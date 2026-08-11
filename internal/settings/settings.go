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
)

type Defaults struct {
	MonitoringSampleIntervalSeconds int
	MonitoringHistoryLimit          int
}

type Values struct {
	Monitoring            Monitoring `json:"monitoring"`
	RestartRequired       bool       `json:"restart_required"`
	RestartRequiredFields []string   `json:"restart_required_fields"`
}

type Monitoring struct {
	SampleIntervalSeconds int `json:"sample_interval_seconds"`
	HistoryLimit          int `json:"history_limit"`
}

type Patch struct {
	Monitoring *MonitoringPatch `json:"monitoring,omitempty"`
}

type MonitoringPatch struct {
	SampleIntervalSeconds *int `json:"sample_interval_seconds,omitempty"`
	HistoryLimit          *int `json:"history_limit,omitempty"`
}

type Service struct {
	db       *sql.DB
	defaults Defaults
	mu       sync.Mutex
}

func New(db *sql.DB, defaults Defaults) *Service {
	if defaults.MonitoringSampleIntervalSeconds == 0 {
		defaults.MonitoringSampleIntervalSeconds = 5
	}
	if defaults.MonitoringHistoryLimit == 0 {
		defaults.MonitoringHistoryLimit = 300
	}
	return &Service{db: db, defaults: defaults}
}

func (s *Service) Get(ctx context.Context) (Values, error) {
	return s.get(ctx, s.db)
}

// Update changes only supplied typed fields and returns their stable API paths.
func (s *Service) Update(ctx context.Context, patch Patch) (Values, []string, error) {
	if patch.Monitoring == nil || (patch.Monitoring.SampleIntervalSeconds == nil && patch.Monitoring.HistoryLimit == nil) {
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
	changed := make([]string, 0, 2)
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
	for _, key := range changed {
		var value int
		if key == monitoringSampleIntervalKey {
			value = current.Monitoring.SampleIntervalSeconds
		} else {
			value = current.Monitoring.HistoryLimit
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, strconv.Itoa(value), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return Values{}, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Values{}, nil, err
	}
	return current, changed, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Service) get(ctx context.Context, q queryer) (Values, error) {
	values := Values{Monitoring: Monitoring{SampleIntervalSeconds: s.defaults.MonitoringSampleIntervalSeconds, HistoryLimit: s.defaults.MonitoringHistoryLimit}, RestartRequired: true, RestartRequiredFields: []string{monitoringSampleIntervalKey, monitoringHistoryLimitKey}}
	if err := validateInterval(values.Monitoring.SampleIntervalSeconds); err != nil {
		return Values{}, fmt.Errorf("invalid monitoring default: %w", err)
	}
	if err := validateHistoryLimit(values.Monitoring.HistoryLimit); err != nil {
		return Values{}, fmt.Errorf("invalid monitoring default: %w", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT key,value FROM app_settings WHERE key IN (?,?)`, monitoringSampleIntervalKey, monitoringHistoryLimitKey)
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
