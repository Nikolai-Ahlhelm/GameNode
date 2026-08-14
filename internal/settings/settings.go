// Package settings provides the typed, whitelisted application settings used
// by the management API. It deliberately does not expose arbitrary config.
package settings

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image/png"
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
	passwordMinimumLengthKey    = "security.password_minimum_length"
	passwordMaximumLengthKey    = "security.password_maximum_length"
	brandingNameKey             = "branding.name"
	brandingSubtitleKey         = "branding.subtitle"
	brandingFaviconTypeKey      = "branding.favicon_content_type"
	brandingFaviconDataKey      = "branding.favicon_data"
	MaxFaviconBytes             = 256 << 10
)

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
	RestartRequired       bool       `json:"restart_required"`
	RestartRequiredFields []string   `json:"restart_required_fields"`
}

type Monitoring struct {
	SampleIntervalSeconds int `json:"sample_interval_seconds"`
	HistoryLimit          int `json:"history_limit"`
}

type Logging struct {
	Level string `json:"level"`
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
}

type LoggingPatch struct {
	Level *string `json:"level,omitempty"`
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
	if (patch.Monitoring == nil || (patch.Monitoring.SampleIntervalSeconds == nil && patch.Monitoring.HistoryLimit == nil)) && (patch.Logging == nil || patch.Logging.Level == nil) && (patch.Security == nil || (patch.Security.PasswordMinimumLength == nil && patch.Security.PasswordMaximumLength == nil)) && (patch.Branding == nil || (patch.Branding.Name == nil && patch.Branding.Subtitle == nil)) {
		values, err := s.Get(ctx)
		return values, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Values{}, nil, err
	}
	defer tx.Rollback()
	current, err := s.get(ctx, tx)
	if err != nil {
		return Values{}, nil, err
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
	if value := patch.Logging; value != nil && value.Level != nil && *value.Level != current.Logging.Level {
		if err := validateLogLevel(*value.Level); err != nil {
			return Values{}, nil, err
		}
		current.Logging.Level = *value.Level
		changed = append(changed, loggingLevelKey)
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
	for _, key := range changed {
		var stored string
		if key == monitoringSampleIntervalKey {
			stored = strconv.Itoa(current.Monitoring.SampleIntervalSeconds)
		} else if key == monitoringHistoryLimitKey {
			stored = strconv.Itoa(current.Monitoring.HistoryLimit)
		} else if key == loggingLevelKey {
			stored = current.Logging.Level
		} else if key == passwordMinimumLengthKey {
			stored = strconv.Itoa(current.Security.PasswordMinimumLength)
		} else if key == passwordMaximumLengthKey {
			stored = strconv.Itoa(current.Security.PasswordMaximumLength)
		} else if key == brandingNameKey {
			stored = current.Branding.Name
		} else {
			stored = current.Branding.Subtitle
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, stored, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return Values{}, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Values{}, nil, err
	}
	if len(changed) > 0 && s.onUpdate != nil {
		s.onUpdate(current, changed)
	}
	return current, changed, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Service) get(ctx context.Context, q queryer) (Values, error) {
	values := Values{Monitoring: Monitoring{SampleIntervalSeconds: s.defaults.MonitoringSampleIntervalSeconds, HistoryLimit: s.defaults.MonitoringHistoryLimit}, Logging: Logging{Level: s.defaults.LoggingLevel}, Security: Security{PasswordMinimumLength: s.defaults.PasswordMinimumLength, PasswordMaximumLength: s.defaults.PasswordMaximumLength}, Branding: Branding{Name: s.defaults.BrandingName, Subtitle: s.defaults.BrandingSubtitle}, RestartRequired: true, RestartRequiredFields: []string{monitoringSampleIntervalKey, monitoringHistoryLimitKey}}
	if err := validateInterval(values.Monitoring.SampleIntervalSeconds); err != nil {
		return Values{}, fmt.Errorf("invalid monitoring default: %w", err)
	}
	if err := validateHistoryLimit(values.Monitoring.HistoryLimit); err != nil {
		return Values{}, fmt.Errorf("invalid monitoring default: %w", err)
	}
	if err := validatePasswordLengths(values.Security.PasswordMinimumLength, values.Security.PasswordMaximumLength); err != nil {
		return Values{}, fmt.Errorf("invalid password policy default: %w", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT key,value FROM app_settings WHERE key IN (?,?,?,?,?,?,?,?)`, monitoringSampleIntervalKey, monitoringHistoryLimitKey, loggingLevelKey, passwordMinimumLengthKey, passwordMaximumLengthKey, brandingNameKey, brandingSubtitleKey, brandingFaviconDataKey)
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
		case brandingFaviconDataKey:
			values.Branding.CustomFavicon = raw != ""
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
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, value := range map[string]string{brandingFaviconTypeKey: contentType, brandingFaviconDataKey: base64.StdEncoding.EncodeToString(data)} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, now); err != nil {
			return "", err
		}
	}
	return contentType, tx.Commit()
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
