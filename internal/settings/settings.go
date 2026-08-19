// Package settings provides the typed, whitelisted application settings used
// by the management API. It deliberately does not expose arbitrary config.
package settings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	monitoringSampleIntervalKey = "monitoring.sample_interval_seconds"
	monitoringHistoryLimitKey   = "monitoring.history_limit"
	loggingLevelKey             = "logging.level"
	loggingDetailedErrorsKey    = "logging.detailed_errors"
	passwordMinimumLengthKey    = "security.password_minimum_length"
	passwordMaximumLengthKey    = "security.password_maximum_length"
	brandingNameKey             = "branding.name"
	brandingSubtitleKey         = "branding.subtitle"
	containerImageAllowlistKey  = "runtime.container_image_allowlist"
	brandingFaviconTypeKey      = "branding.favicon_content_type"
	brandingFaviconDataKey      = "branding.favicon_data"
	MaxFaviconBytes             = 256 << 10
)

// logCategoryKeys is the strict whitelist of persisted log category toggles.
// It intentionally mirrors internal/logging.Categories, but settings does not
// import logging - composition happens in main/api so this package stays
// free of any HTTP or logging-framework dependency.
var logCategoryKeys = []struct {
	name string
	key  string
}{
	{"http", "logging.category.http"},
	{"database", "logging.category.database"},
	{"runtime", "logging.category.runtime"},
	{"auth", "logging.category.auth"},
	{"filesystem", "logging.category.filesystem"},
	{"provisioning", "logging.category.provisioning"},
	{"steamcmd", "logging.category.steamcmd"},
	{"templates", "logging.category.templates"},
	{"general", "logging.category.general"},
}

// ErrPersistence marks a settings failure that originated in the database
// layer rather than input validation. Callers (the API layer) use it to
// decide the API response and never forward the wrapped driver error to a
// client - only a detailed-error-gated application log may see it, via
// logging.ErrorDetail.
var ErrPersistence = errors.New("settings could not be persisted")

func categoryKey(name string) string {
	for _, entry := range logCategoryKeys {
		if entry.name == name {
			return entry.key
		}
	}
	return ""
}
func categoryNameForKey(key string) (string, bool) {
	for _, entry := range logCategoryKeys {
		if entry.key == key {
			return entry.name, true
		}
	}
	return "", false
}
func allCategoryKeys() []string {
	keys := make([]string, len(logCategoryKeys))
	for i, entry := range logCategoryKeys {
		keys[i] = entry.key
	}
	return keys
}

type Defaults struct {
	MonitoringSampleIntervalSeconds int
	MonitoringHistoryLimit          int
	LoggingLevel                    string
	PasswordMinimumLength           int
	PasswordMaximumLength           int
	BrandingName                    string
	BrandingSubtitle                string
}

type Values struct {
	Monitoring            Monitoring `json:"monitoring"`
	Logging               Logging    `json:"logging"`
	Security              Security   `json:"security"`
	Branding              Branding   `json:"branding"`
	Runtime               Runtime    `json:"runtime"`
	RestartRequired       bool       `json:"restart_required"`
	RestartRequiredFields []string   `json:"restart_required_fields"`
}

type Runtime struct {
	ContainerImageAllowlist []string `json:"container_image_allowlist"`
}

type Monitoring struct {
	SampleIntervalSeconds int `json:"sample_interval_seconds"`
	HistoryLimit          int `json:"history_limit"`
}

type Logging struct {
	Level          string            `json:"level"`
	Categories     LoggingCategories `json:"categories"`
	DetailedErrors bool              `json:"detailed_errors"`
}

// LoggingCategories is a strict, fixed whitelist of togglable log
// categories - not arbitrary logger configuration. Each field maps 1:1 to a
// category the logging package recognizes.
type LoggingCategories struct {
	HTTP         bool `json:"http"`
	Database     bool `json:"database"`
	Runtime      bool `json:"runtime"`
	Auth         bool `json:"auth"`
	Filesystem   bool `json:"filesystem"`
	Provisioning bool `json:"provisioning"`
	SteamCMD     bool `json:"steamcmd"`
	Templates    bool `json:"templates"`
	General      bool `json:"general"`
}

// AsMap returns the categories keyed by their logging-package category name,
// for handoff to logging.Manager.SetCategories without either package
// depending on the other.
func (c LoggingCategories) AsMap() map[string]bool {
	return map[string]bool{
		"http": c.HTTP, "database": c.Database, "runtime": c.Runtime, "auth": c.Auth,
		"filesystem": c.Filesystem, "provisioning": c.Provisioning, "steamcmd": c.SteamCMD,
		"templates": c.Templates, "general": c.General,
	}
}

func (c *LoggingCategories) set(name string, value bool) {
	switch name {
	case "http":
		c.HTTP = value
	case "database":
		c.Database = value
	case "runtime":
		c.Runtime = value
	case "auth":
		c.Auth = value
	case "filesystem":
		c.Filesystem = value
	case "provisioning":
		c.Provisioning = value
	case "steamcmd":
		c.SteamCMD = value
	case "templates":
		c.Templates = value
	case "general":
		c.General = value
	}
}
func (c LoggingCategories) get(name string) bool {
	switch name {
	case "http":
		return c.HTTP
	case "database":
		return c.Database
	case "runtime":
		return c.Runtime
	case "auth":
		return c.Auth
	case "filesystem":
		return c.Filesystem
	case "provisioning":
		return c.Provisioning
	case "steamcmd":
		return c.SteamCMD
	case "templates":
		return c.Templates
	case "general":
		return c.General
	default:
		return false
	}
}

type Security struct {
	PasswordMinimumLength int `json:"password_minimum_length"`
	PasswordMaximumLength int `json:"password_maximum_length"`
}

type Branding struct {
	Name          string `json:"name"`
	Subtitle      string `json:"subtitle"`
	CustomFavicon bool   `json:"custom_favicon"`
}

type Patch struct {
	Monitoring *MonitoringPatch `json:"monitoring,omitempty"`
	Logging    *LoggingPatch    `json:"logging,omitempty"`
	Security   *SecurityPatch   `json:"security,omitempty"`
	Branding   *BrandingPatch   `json:"branding,omitempty"`
	Runtime    *RuntimePatch    `json:"runtime,omitempty"`
}

type RuntimePatch struct {
	ContainerImageAllowlist *[]string `json:"container_image_allowlist,omitempty"`
}

type LoggingPatch struct {
	Level          *string                 `json:"level,omitempty"`
	Categories     *LoggingCategoriesPatch `json:"categories,omitempty"`
	DetailedErrors *bool                   `json:"detailed_errors,omitempty"`
}

// LoggingCategoriesPatch mirrors LoggingCategories with optional fields so a
// PATCH can change one category without touching the others. Any field not
// present in the request body is decoded as nil and left unchanged; unknown
// field names are rejected by the request decoder's DisallowUnknownFields.
type LoggingCategoriesPatch struct {
	HTTP         *bool `json:"http,omitempty"`
	Database     *bool `json:"database,omitempty"`
	Runtime      *bool `json:"runtime,omitempty"`
	Auth         *bool `json:"auth,omitempty"`
	Filesystem   *bool `json:"filesystem,omitempty"`
	Provisioning *bool `json:"provisioning,omitempty"`
	SteamCMD     *bool `json:"steamcmd,omitempty"`
	Templates    *bool `json:"templates,omitempty"`
	General      *bool `json:"general,omitempty"`
}

func (p *LoggingCategoriesPatch) isEmpty() bool {
	return p == nil || (p.HTTP == nil && p.Database == nil && p.Runtime == nil && p.Auth == nil && p.Filesystem == nil && p.Provisioning == nil && p.SteamCMD == nil && p.Templates == nil && p.General == nil)
}

// entries returns the patch as (category name, value) pairs for fields that
// were actually supplied.
func (p *LoggingCategoriesPatch) entries() []struct {
	name  string
	value bool
} {
	var out []struct {
		name  string
		value bool
	}
	if p == nil {
		return out
	}
	add := func(name string, value *bool) {
		if value != nil {
			out = append(out, struct {
				name  string
				value bool
			}{name, *value})
		}
	}
	add("http", p.HTTP)
	add("database", p.Database)
	add("runtime", p.Runtime)
	add("auth", p.Auth)
	add("filesystem", p.Filesystem)
	add("provisioning", p.Provisioning)
	add("steamcmd", p.SteamCMD)
	add("templates", p.Templates)
	add("general", p.General)
	return out
}

type MonitoringPatch struct {
	SampleIntervalSeconds *int `json:"sample_interval_seconds,omitempty"`
	HistoryLimit          *int `json:"history_limit,omitempty"`
}

type SecurityPatch struct {
	PasswordMinimumLength *int `json:"password_minimum_length,omitempty"`
	PasswordMaximumLength *int `json:"password_maximum_length,omitempty"`
}

type BrandingPatch struct {
	Name     *string `json:"name,omitempty"`
	Subtitle *string `json:"subtitle,omitempty"`
}

type Service struct {
	db       *sql.DB
	defaults Defaults
	mu       sync.Mutex
	onUpdate func(Values, []string)
}

func New(db *sql.DB, defaults Defaults) *Service {
	if defaults.MonitoringSampleIntervalSeconds == 0 {
		defaults.MonitoringSampleIntervalSeconds = 5
	}
	if defaults.MonitoringHistoryLimit == 0 {
		defaults.MonitoringHistoryLimit = 300
	}
	if defaults.LoggingLevel == "" {
		defaults.LoggingLevel = "info"
	}
	if defaults.PasswordMinimumLength == 0 {
		defaults.PasswordMinimumLength = 8
	}
	if defaults.PasswordMaximumLength == 0 {
		defaults.PasswordMaximumLength = 256
	}
	if defaults.BrandingName == "" {
		defaults.BrandingName = "GameNode"
	}
	if defaults.BrandingSubtitle == "" {
		defaults.BrandingSubtitle = "Infrastructure manager"
	}
	if err := validateLogLevel(defaults.LoggingLevel); err != nil {
		defaults.LoggingLevel = "info"
	}
	return &Service{db: db, defaults: defaults}
}

// SetOnUpdate installs the process-local settings hook (for example, the
// live logging level). It is deliberately not persisted as arbitrary code.
func (s *Service) SetOnUpdate(callback func(Values, []string)) { s.onUpdate = callback }

func (s *Service) Get(ctx context.Context) (Values, error) {
	return s.get(ctx, s.db)
}

// PasswordPolicy returns the effective live password length policy for
// authentication and identity services.
func (s *Service) PasswordPolicy(ctx context.Context) (int, int, error) {
	values, err := s.Get(ctx)
	return values.Security.PasswordMinimumLength, values.Security.PasswordMaximumLength, err
}

// Update changes only supplied typed fields and returns their stable API paths.
func (s *Service) Update(ctx context.Context, patch Patch) (Values, []string, error) {
	if (patch.Monitoring == nil || (patch.Monitoring.SampleIntervalSeconds == nil && patch.Monitoring.HistoryLimit == nil)) && (patch.Logging == nil || (patch.Logging.Level == nil && patch.Logging.DetailedErrors == nil && patch.Logging.Categories.isEmpty())) && (patch.Security == nil || (patch.Security.PasswordMinimumLength == nil && patch.Security.PasswordMaximumLength == nil)) && (patch.Branding == nil || (patch.Branding.Name == nil && patch.Branding.Subtitle == nil)) && (patch.Runtime == nil || patch.Runtime.ContainerImageAllowlist == nil) {
		values, err := s.Get(ctx)
		return values, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Values{}, nil, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	defer tx.Rollback()
	current, err := s.get(ctx, tx)
	if err != nil {
		return Values{}, nil, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	changed := make([]string, 0, 3)
	if patch.Monitoring != nil {
		if value := patch.Monitoring.SampleIntervalSeconds; value != nil && *value != current.Monitoring.SampleIntervalSeconds {
			if err := validateInterval(*value); err != nil {
				return Values{}, nil, err
			}
			current.Monitoring.SampleIntervalSeconds = *value
			changed = append(changed, monitoringSampleIntervalKey)
		}
		if value := patch.Monitoring.HistoryLimit; value != nil && *value != current.Monitoring.HistoryLimit {
			if err := validateHistoryLimit(*value); err != nil {
				return Values{}, nil, err
			}
			current.Monitoring.HistoryLimit = *value
			changed = append(changed, monitoringHistoryLimitKey)
		}
	}
	if value := patch.Logging; value != nil {
		if value.Level != nil && *value.Level != current.Logging.Level {
			if err := validateLogLevel(*value.Level); err != nil {
				return Values{}, nil, err
			}
			current.Logging.Level = *value.Level
			changed = append(changed, loggingLevelKey)
		}
		if value.DetailedErrors != nil && *value.DetailedErrors != current.Logging.DetailedErrors {
			current.Logging.DetailedErrors = *value.DetailedErrors
			changed = append(changed, loggingDetailedErrorsKey)
		}
		for _, entry := range value.Categories.entries() {
			if current.Logging.Categories.get(entry.name) == entry.value {
				continue
			}
			current.Logging.Categories.set(entry.name, entry.value)
			changed = append(changed, categoryKey(entry.name))
		}
	}
	if patch.Security != nil {
		minimum, maximum := current.Security.PasswordMinimumLength, current.Security.PasswordMaximumLength
		if patch.Security.PasswordMinimumLength != nil {
			minimum = *patch.Security.PasswordMinimumLength
		}
		if patch.Security.PasswordMaximumLength != nil {
			maximum = *patch.Security.PasswordMaximumLength
		}
		if err := validatePasswordLengths(minimum, maximum); err != nil {
			return Values{}, nil, err
		}
		if minimum != current.Security.PasswordMinimumLength {
			current.Security.PasswordMinimumLength = minimum
			changed = append(changed, passwordMinimumLengthKey)
		}
		if maximum != current.Security.PasswordMaximumLength {
			current.Security.PasswordMaximumLength = maximum
			changed = append(changed, passwordMaximumLengthKey)
		}
	}
	if patch.Branding != nil {
		if patch.Branding.Name != nil {
			name, err := validateBrandingText(*patch.Branding.Name, 1, 64, "instance name")
			if err != nil {
				return Values{}, nil, err
			}
			if name != current.Branding.Name {
				current.Branding.Name = name
				changed = append(changed, brandingNameKey)
			}
		}
		if patch.Branding.Subtitle != nil {
			subtitle, err := validateBrandingText(*patch.Branding.Subtitle, 0, 128, "instance subtitle")
			if err != nil {
				return Values{}, nil, err
			}
			if subtitle != current.Branding.Subtitle {
				current.Branding.Subtitle = subtitle
				changed = append(changed, brandingSubtitleKey)
			}
		}
	}
	if patch.Runtime != nil && patch.Runtime.ContainerImageAllowlist != nil {
		allowlist, err := validateImageAllowlist(*patch.Runtime.ContainerImageAllowlist)
		if err != nil {
			return Values{}, nil, err
		}
		if !sameStrings(allowlist, current.Runtime.ContainerImageAllowlist) {
			current.Runtime.ContainerImageAllowlist = allowlist
			changed = append(changed, containerImageAllowlistKey)
		}
	}
	for _, key := range changed {
		var stored string
		switch {
		case key == monitoringSampleIntervalKey:
			stored = strconv.Itoa(current.Monitoring.SampleIntervalSeconds)
		case key == monitoringHistoryLimitKey:
			stored = strconv.Itoa(current.Monitoring.HistoryLimit)
		case key == loggingLevelKey:
			stored = current.Logging.Level
		case key == loggingDetailedErrorsKey:
			stored = strconv.FormatBool(current.Logging.DetailedErrors)
		case key == passwordMinimumLengthKey:
			stored = strconv.Itoa(current.Security.PasswordMinimumLength)
		case key == passwordMaximumLengthKey:
			stored = strconv.Itoa(current.Security.PasswordMaximumLength)
		case key == brandingNameKey:
			stored = current.Branding.Name
		case key == brandingSubtitleKey:
			stored = current.Branding.Subtitle
		case key == containerImageAllowlistKey:
			encoded, _ := json.Marshal(current.Runtime.ContainerImageAllowlist)
			stored = string(encoded)
		default:
			if name, ok := categoryNameForKey(key); ok {
				stored = strconv.FormatBool(current.Logging.Categories.get(name))
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, stored, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return Values{}, nil, fmt.Errorf("%w: %v", ErrPersistence, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Values{}, nil, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	if len(changed) > 0 && s.onUpdate != nil {
		s.onUpdate(current, changed)
	}
	return current, changed, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// defaultLoggingCategories preserves useful operational and error logging by
// default. Routine HTTP access entries are already demoted to debug level
// (see api.Server.logRequests), so no category needs to start disabled.
func defaultLoggingCategories() LoggingCategories {
	return LoggingCategories{HTTP: true, Database: true, Runtime: true, Auth: true, Filesystem: true, Provisioning: true, SteamCMD: true, Templates: true, General: true}
}

func (s *Service) get(ctx context.Context, q queryer) (Values, error) {
	values := Values{Monitoring: Monitoring{SampleIntervalSeconds: s.defaults.MonitoringSampleIntervalSeconds, HistoryLimit: s.defaults.MonitoringHistoryLimit}, Logging: Logging{Level: s.defaults.LoggingLevel, Categories: defaultLoggingCategories(), DetailedErrors: false}, Security: Security{PasswordMinimumLength: s.defaults.PasswordMinimumLength, PasswordMaximumLength: s.defaults.PasswordMaximumLength}, Branding: Branding{Name: s.defaults.BrandingName, Subtitle: s.defaults.BrandingSubtitle}, Runtime: Runtime{ContainerImageAllowlist: []string{"docker.io", "ghcr.io", "quay.io"}}, RestartRequired: true, RestartRequiredFields: []string{monitoringSampleIntervalKey, monitoringHistoryLimitKey}}
	if err := validateInterval(values.Monitoring.SampleIntervalSeconds); err != nil {
		return Values{}, fmt.Errorf("invalid monitoring default: %w", err)
	}
	if err := validateHistoryLimit(values.Monitoring.HistoryLimit); err != nil {
		return Values{}, fmt.Errorf("invalid monitoring default: %w", err)
	}
	if err := validatePasswordLengths(values.Security.PasswordMinimumLength, values.Security.PasswordMaximumLength); err != nil {
		return Values{}, fmt.Errorf("invalid password policy default: %w", err)
	}
	keys := append([]string{monitoringSampleIntervalKey, monitoringHistoryLimitKey, loggingLevelKey, loggingDetailedErrorsKey, passwordMinimumLengthKey, passwordMaximumLengthKey, brandingNameKey, brandingSubtitleKey, brandingFaviconDataKey, containerImageAllowlistKey}, allCategoryKeys()...)
	placeholders := strings.Repeat("?,", len(keys))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}
	rows, err := q.QueryContext(ctx, `SELECT key,value FROM app_settings WHERE key IN (`+placeholders+`)`, args...)
	if err != nil {
		return Values{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return Values{}, err
		}
		switch key {
		case monitoringSampleIntervalKey:
			value, err := strconv.Atoi(raw)
			if err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q", key)
			}
			if err := validateInterval(value); err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q: %w", key, err)
			}
			values.Monitoring.SampleIntervalSeconds = value
		case monitoringHistoryLimitKey:
			value, err := strconv.Atoi(raw)
			if err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q", key)
			}
			if err := validateHistoryLimit(value); err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q: %w", key, err)
			}
			values.Monitoring.HistoryLimit = value
		case loggingLevelKey:
			if err := validateLogLevel(raw); err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q: %w", key, err)
			}
			values.Logging.Level = raw
		case loggingDetailedErrorsKey:
			value, err := strconv.ParseBool(raw)
			if err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q", key)
			}
			values.Logging.DetailedErrors = value
		case passwordMinimumLengthKey:
			value, err := strconv.Atoi(raw)
			if err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q", key)
			}
			values.Security.PasswordMinimumLength = value
		case passwordMaximumLengthKey:
			value, err := strconv.Atoi(raw)
			if err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q", key)
			}
			values.Security.PasswordMaximumLength = value
		case brandingNameKey:
			values.Branding.Name = raw
		case brandingSubtitleKey:
			values.Branding.Subtitle = raw
		case containerImageAllowlistKey:
			var allowlist []string
			if json.Unmarshal([]byte(raw), &allowlist) != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q", key)
			}
			if values.Runtime.ContainerImageAllowlist, err = validateImageAllowlist(allowlist); err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q: %w", key, err)
			}
		case brandingFaviconDataKey:
			values.Branding.CustomFavicon = raw != ""
		default:
			if name, ok := categoryNameForKey(key); ok {
				value, err := strconv.ParseBool(raw)
				if err != nil {
					return Values{}, fmt.Errorf("invalid persisted setting %q", key)
				}
				values.Logging.Categories.set(name, value)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Values{}, err
	}
	if err := validatePasswordLengths(values.Security.PasswordMinimumLength, values.Security.PasswordMaximumLength); err != nil {
		return Values{}, fmt.Errorf("invalid persisted password policy: %w", err)
	}
	if _, err := validateBrandingText(values.Branding.Name, 1, 64, "instance name"); err != nil {
		return Values{}, fmt.Errorf("invalid persisted branding: %w", err)
	}
	if _, err := validateBrandingText(values.Branding.Subtitle, 0, 128, "instance subtitle"); err != nil {
		return Values{}, fmt.Errorf("invalid persisted branding: %w", err)
	}
	return values, nil
}

func validateInterval(v int) error {
	if v < 1 || v > 300 {
		return errors.New("monitoring sample interval must be between 1 and 300 seconds")
	}
	return nil
}
func validateHistoryLimit(v int) error {
	if v < 1 || v > 10000 {
		return errors.New("monitoring history limit must be between 1 and 10000")
	}
	return nil
}

func validateLogLevel(value string) error {
	switch value {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return errors.New("log level must be debug, info, warn, or error")
	}
}

func validatePasswordLengths(minimum, maximum int) error {
	if minimum < 8 || minimum > 128 {
		return errors.New("password minimum length must be between 8 and 128 characters")
	}
	if maximum < minimum || maximum > 256 {
		return errors.New("password maximum length must be between the minimum length and 256 characters")
	}
	return nil
}

func validateBrandingText(value string, minimum, maximum int, field string) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < minimum || utf8.RuneCountInString(value) > maximum {
		return "", fmt.Errorf("%s must be between %d and %d characters", field, minimum, maximum)
	}
	return value, nil
}

var registryName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,126}[a-z0-9])?(?::[0-9]{1,5})?$`)

func validateImageAllowlist(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 32 {
		return nil, errors.New("container image allowlist must contain between 1 and 32 registries")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !registryName.MatchString(value) || seen[value] {
			return nil, errors.New("container image allowlist contains an invalid registry")
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Service) SetFavicon(ctx context.Context, data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxFaviconBytes {
		return "", errors.New("favicon must be between 1 byte and 256 KiB")
	}
	contentType := ""
	if bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 512 || config.Height > 512 {
			return "", errors.New("favicon PNG must be valid and no larger than 512 by 512 pixels")
		}
		contentType = "image/png"
	}
	if len(data) >= 6 && bytes.Equal(data[:4], []byte{0, 0, 1, 0}) {
		count := int(binary.LittleEndian.Uint16(data[4:6]))
		if count < 1 || count > 20 || len(data) < 6+count*16 {
			return "", errors.New("favicon ICO directory is invalid")
		}
		for index := 0; index < count; index++ {
			entry := data[6+index*16:]
			size := uint64(binary.LittleEndian.Uint32(entry[8:12]))
			offset := uint64(binary.LittleEndian.Uint32(entry[12:16]))
			if size == 0 || offset < uint64(6+count*16) || offset+size > uint64(len(data)) {
				return "", errors.New("favicon ICO entry is invalid")
			}
		}
		contentType = "image/x-icon"
	}
	if contentType == "" {
		return "", errors.New("favicon must be a valid PNG or ICO file")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, value := range map[string]string{brandingFaviconTypeKey: contentType, brandingFaviconDataKey: base64.StdEncoding.EncodeToString(data)} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, now); err != nil {
			return "", fmt.Errorf("%w: %v", ErrPersistence, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	return contentType, nil
}

func (s *Service) Favicon(ctx context.Context) (string, []byte, error) {
	var contentType, encoded string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key=?`, brandingFaviconTypeKey).Scan(&contentType); err != nil {
		return "", nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key=?`, brandingFaviconDataKey).Scan(&encoded); err != nil {
		return "", nil, err
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > MaxFaviconBytes {
		return "", nil, errors.New("invalid persisted favicon")
	}
	return contentType, data, nil
}

func (s *Service) DeleteFavicon(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_settings WHERE key IN (?,?)`, brandingFaviconTypeKey, brandingFaviconDataKey)
	return err
}
