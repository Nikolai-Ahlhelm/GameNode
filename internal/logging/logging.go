// Package logging provides the bounded, human-readable application logger.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxHistoryEntries = 1000
	maxFileBytes      = 4 << 20
	maxFileBackups    = 4
)

// Log categories are a lightweight, whitelisted grouping layered on top of
// the existing debug/info/warn/error levels. They let operators mute a noisy
// but non-critical operational surface (for example routine HTTP access
// entries) without losing everything else at the same level. Callers opt a
// logger into a category with WithCategory; entries without a category fall
// back to CategoryGeneral so the toggle always has an observable effect.
const (
	CategoryHTTP         = "http"
	CategoryDatabase     = "database"
	CategoryRuntime      = "runtime"
	CategoryAuth         = "auth"
	CategoryFilesystem   = "filesystem"
	CategoryProvisioning = "provisioning"
	CategorySteamCMD     = "steamcmd"
	CategoryTemplates    = "templates"
	CategoryGeneral      = "general"

	categoryAttrKey = "category"

	// UnderlyingErrorKey is the structured field name used to carry the raw
	// error returned by a trusted internal library or service (for example a
	// SQLite driver error). The handler only emits this field when detailed
	// error logging is enabled; callers can attach it unconditionally via
	// ErrorDetail without needing to know the current setting themselves.
	UnderlyingErrorKey = "underlying_error"
)

// Categories lists every valid category, in the order they are presented to
// operators. It is the single source of truth other packages (settings, API)
// validate against - the set is deliberately fixed, not extensible at runtime.
var Categories = []string{CategoryHTTP, CategoryDatabase, CategoryRuntime, CategoryAuth, CategoryFilesystem, CategoryProvisioning, CategorySteamCMD, CategoryTemplates, CategoryGeneral}

// ValidCategory reports whether name is one of the fixed, whitelisted
// categories above.
func ValidCategory(name string) bool {
	for _, c := range Categories {
		if c == name {
			return true
		}
	}
	return false
}

// WithCategory returns a logger that tags every entry it produces with the
// given category. It is intended to be called once per subsystem (for
// example when wiring a service's logger in main), not per log line.
func WithCategory(log *slog.Logger, category string) *slog.Logger {
	return log.With(categoryAttrKey, category)
}

// ErrorDetail returns a structured attribute carrying the raw underlying
// error. It is safe to attach unconditionally at any call site: the handler
// strips it before it reaches the log file, in-memory history, or console
// output unless detailed error logging is enabled. Callers must only pass
// errors from trusted internal libraries/services (for example a database
// driver) - never request input - since detailed logging does not perform
// any additional secret redaction beyond what the error already omits.
func ErrorDetail(err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}
	return slog.String(UnderlyingErrorKey, err.Error())
}

// Entry is a single in-memory application log line. The buffer is process
// local only and is deliberately not restored from log files at startup.
type Entry struct {
	Level string `json:"level"`
	Line  string `json:"line"`
}

// Manager owns the application log destination and its live level switch.
// Files are deliberately opened for each write so clearing old log files is
// safe on Windows as well as Linux.
type Manager struct {
	directory  string
	level      *slog.LevelVar
	mu         sync.Mutex
	historyMu  sync.RWMutex
	history    []Entry
	categoryMu sync.RWMutex
	// categories is nil until SetCategories is first called, in which case
	// every category is enabled - matching the documented default of not
	// hiding anything until an operator opts in.
	categories     map[string]bool
	detailedErrors atomic.Bool
}

func New(directory, level string) (*Manager, *slog.Logger, error) {
	if directory == "" {
		return nil, nil, fmt.Errorf("log directory is required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	m := &Manager{directory: directory, level: new(slog.LevelVar)}
	if err := m.SetLevel(level); err != nil {
		return nil, nil, err
	}
	return m, slog.New(&handler{manager: m}), nil
}

// SetCategories replaces the live enable/disable state for log categories.
// Unlisted categories keep their previous state (or default to enabled if
// never set). The map's keys must all be valid, whitelisted categories.
func (m *Manager) SetCategories(enabled map[string]bool) error {
	for name := range enabled {
		if !ValidCategory(name) {
			return fmt.Errorf("unknown log category %q", name)
		}
	}
	m.categoryMu.Lock()
	defer m.categoryMu.Unlock()
	if m.categories == nil {
		m.categories = make(map[string]bool, len(Categories))
	}
	for _, name := range Categories {
		if value, ok := enabled[name]; ok {
			m.categories[name] = value
		} else if _, ok := m.categories[name]; !ok {
			m.categories[name] = true
		}
	}
	return nil
}

// CategoryEnabled reports whether entries tagged with the given category
// should be recorded. An empty or unrecognized category is always enabled.
func (m *Manager) CategoryEnabled(category string) bool {
	if category == "" {
		return true
	}
	m.categoryMu.RLock()
	defer m.categoryMu.RUnlock()
	if m.categories == nil {
		return true
	}
	value, ok := m.categories[category]
	return !ok || value
}

// SetDetailedErrors toggles whether ErrorDetail attributes are recorded.
func (m *Manager) SetDetailedErrors(enabled bool) { m.detailedErrors.Store(enabled) }

// DetailedErrors reports the live detailed-error logging setting.
func (m *Manager) DetailedErrors() bool { return m.detailedErrors.Load() }

func (m *Manager) Directory() string { return m.directory }

// Entries returns a bounded copy of the current-process log buffer.
func (m *Manager) Entries() []Entry {
	m.historyMu.RLock()
	defer m.historyMu.RUnlock()
	return append([]Entry(nil), m.history...)
}

func (m *Manager) remember(entry Entry) {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	if len(m.history) == MaxHistoryEntries {
		copy(m.history, m.history[1:])
		m.history[len(m.history)-1] = entry
		return
	}
	m.history = append(m.history, entry)
}

func (m *Manager) SetLevel(value string) error {
	switch strings.ToLower(value) {
	case "debug":
		m.level.Set(slog.LevelDebug)
	case "info":
		m.level.Set(slog.LevelInfo)
	case "warn", "warning":
		m.level.Set(slog.LevelWarn)
	case "error":
		m.level.Set(slog.LevelError)
	default:
		return fmt.Errorf("log level must be debug, info, warn, or error")
	}
	return nil
}

// Clear removes only regular files directly inside the configured log
// directory. It neither follows links nor accepts a caller-provided path.
func (m *Manager) Clear(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := os.ReadDir(m.directory)
	if err != nil {
		return fmt.Errorf("read log directory: %w", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if err := os.Remove(filepath.Join(m.directory, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove log file: %w", err)
		}
	}
	return nil
}

// ClearExceptCurrent removes historic log files while keeping the current
// process's daily file available for the audit record emitted after clearing.
func (m *Manager) ClearExceptCurrent(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := os.ReadDir(m.directory)
	if err != nil {
		return fmt.Errorf("read log directory: %w", err)
	}
	current := "gamenode-" + time.Now().Format("2006-01-02") + ".log"
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == current || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if err := os.Remove(filepath.Join(m.directory, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove log file: %w", err)
		}
	}
	return nil
}

func (m *Manager) write(line string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := "gamenode-" + time.Now().Format("2006-01-02") + ".log"
	path := filepath.Join(m.directory, name)
	if info, err := os.Stat(path); err == nil && info.Size()+int64(len(line)) > maxFileBytes {
		if err := m.rotate(path); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.WriteString(file, line)
	return err
}

func (m *Manager) rotate(path string) error {
	oldest := path + fmt.Sprintf(".%d", maxFileBackups)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for index := maxFileBackups - 1; index >= 1; index-- {
		from, to := path+fmt.Sprintf(".%d", index), path+fmt.Sprintf(".%d", index+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(path, path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

type handler struct {
	manager *Manager
	attrs   []slog.Attr
	groups  []string
}

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.manager.level.Level()
}
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}
func (h *handler) WithGroup(name string) slog.Handler {
	clone := *h
	if name != "" {
		clone.groups = append(append([]string{}, h.groups...), name)
	}
	return &clone
}
func (h *handler) Handle(_ context.Context, record slog.Record) error {
	attrs := append([]slog.Attr{}, h.attrs...)
	record.Attrs(func(attr slog.Attr) bool { attrs = append(attrs, attr); return true })
	module := "App"
	category := ""
	fields := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		attr.Value = attr.Value.Resolve()
		if attr.Key == "" {
			continue
		}
		if attr.Key == "module" && attr.Value.Kind() == slog.KindString {
			module = attr.Value.String()
			continue
		}
		if attr.Key == categoryAttrKey && attr.Value.Kind() == slog.KindString {
			category = attr.Value.String()
			continue
		}
		if attr.Key == UnderlyingErrorKey && !h.manager.DetailedErrors() {
			continue
		}
		key := attr.Key
		if len(h.groups) > 0 {
			key = strings.Join(h.groups, ".") + "." + key
		}
		fields = append(fields, key+"="+formatValue(attr.Value))
	}
	if category == "" {
		category = CategoryGeneral
	}
	// Category toggles only mute routine/info-and-below noise. Warnings and
	// errors are never hidden by a disabled category - a muted "http"
	// category, for example, must not be able to hide a genuine 5xx.
	if record.Level < slog.LevelWarn && !h.manager.CategoryEnabled(category) {
		return nil
	}
	level := record.Level.String()
	line := fmt.Sprintf("[%s] [%s] [%s] %s", record.Time.Format(time.RFC3339Nano), level, module, record.Message)
	if len(fields) > 0 {
		line += " " + strings.Join(fields, " ")
	}
	line += "\n"
	if err := h.manager.write(line); err != nil {
		return err
	}
	h.manager.remember(Entry{Level: level, Line: strings.TrimSuffix(line, "\n")})
	// Console output is intentionally colored only at the level tag.
	colored := line
	if record.Level >= slog.LevelError {
		colored = strings.Replace(line, "["+level+"]", "\x1b[31m["+level+"]\x1b[0m", 1)
	} else if record.Level >= slog.LevelWarn {
		colored = strings.Replace(line, "["+level+"]", "\x1b[33m["+level+"]\x1b[0m", 1)
	}
	_, err := io.WriteString(os.Stdout, colored)
	return err
}

func formatValue(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return fmt.Sprintf("%q", value.String())
	default:
		return value.String()
	}
}
