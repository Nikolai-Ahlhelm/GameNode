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
	"time"
)

const MaxHistoryEntries = 1000

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
	directory string
	level     *slog.LevelVar
	mu        sync.Mutex
	historyMu sync.RWMutex
	history   []Entry
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
	file, err := os.OpenFile(filepath.Join(m.directory, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.WriteString(file, line)
	return err
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
	fields := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		attr.Value = attr.Value.Resolve()
		if attr.Key == "module" && attr.Value.Kind() == slog.KindString {
			module = attr.Value.String()
			continue
		}
		key := attr.Key
		if len(h.groups) > 0 {
			key = strings.Join(h.groups, ".") + "." + key
		}
		fields = append(fields, key+"="+formatValue(attr.Value))
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
