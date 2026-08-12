// Package settings provides the typed, whitelisted application settings used
// by the management API. It deliberately does not expose arbitrary config.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

const (
	monitoringSampleIntervalKey = "monitoring.sample_interval_seconds"
	monitoringHistoryLimitKey   = "monitoring.history_limit"
	loggingLevelKey             = "logging.level"
)

type Defaults struct {
	MonitoringSampleIntervalSeconds int
	MonitoringHistoryLimit          int
	LoggingLevel                    string
}

type Values struct {
	Monitoring            Monitoring `json:"monitoring"`
	Logging               Logging    `json:"logging"`
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

type Patch struct {
	Monitoring *MonitoringPatch `json:"monitoring,omitempty"`
	Logging    *LoggingPatch    `json:"logging,omitempty"`
}

type LoggingPatch struct {
	Level *string `json:"level,omitempty"`
}

type MonitoringPatch struct {
	SampleIntervalSeconds *int `json:"sample_interval_seconds,omitempty"`
	HistoryLimit          *int `json:"history_limit,omitempty"`
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

// Update changes only supplied typed fields and returns their stable API paths.
func (s *Service) Update(ctx context.Context, patch Patch) (Values, []string, error) {
	if (patch.Monitoring == nil || (patch.Monitoring.SampleIntervalSeconds == nil && patch.Monitoring.HistoryLimit == nil)) && (patch.Logging == nil || patch.Logging.Level == nil) {
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
	for _, key := range changed {
		var stored string
		if key == monitoringSampleIntervalKey {
			stored = strconv.Itoa(current.Monitoring.SampleIntervalSeconds)
		} else if key == monitoringHistoryLimitKey {
			stored = strconv.Itoa(current.Monitoring.HistoryLimit)
		} else {
			stored = current.Logging.Level
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
	values := Values{Monitoring: Monitoring{SampleIntervalSeconds: s.defaults.MonitoringSampleIntervalSeconds, HistoryLimit: s.defaults.MonitoringHistoryLimit}, Logging: Logging{Level: s.defaults.LoggingLevel}, RestartRequired: true, RestartRequiredFields: []string{monitoringSampleIntervalKey, monitoringHistoryLimitKey}}
	if err := validateInterval(values.Monitoring.SampleIntervalSeconds); err != nil {
		return Values{}, fmt.Errorf("invalid monitoring default: %w", err)
	}
	if err := validateHistoryLimit(values.Monitoring.HistoryLimit); err != nil {
		return Values{}, fmt.Errorf("invalid monitoring default: %w", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT key,value FROM app_settings WHERE key IN (?,?,?)`, monitoringSampleIntervalKey, monitoringHistoryLimitKey, loggingLevelKey)
	if err != nil {
		return Values{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return Values{}, err
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return Values{}, fmt.Errorf("invalid persisted setting %q", key)
		}
		switch key {
		case monitoringSampleIntervalKey:
			if err := validateInterval(value); err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q: %w", key, err)
			}
			values.Monitoring.SampleIntervalSeconds = value
		case monitoringHistoryLimitKey:
			if err := validateHistoryLimit(value); err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q: %w", key, err)
			}
			values.Monitoring.HistoryLimit = value
		case loggingLevelKey:
			if err := validateLogLevel(raw); err != nil {
				return Values{}, fmt.Errorf("invalid persisted setting %q: %w", key, err)
			}
			values.Logging.Level = raw
		}
	}
	return values, rows.Err()
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
