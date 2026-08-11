// Package diagnostics exposes a safe, read-only node summary without host paths,
// environment data, network enumeration, or command execution.
package diagnostics

import (
	"context"
	"database/sql"
	"runtime"
	"runtime/debug"
	"time"

	"gamenode/internal/settings"
)

type MonitoringEffective struct {
	SampleIntervalSeconds int
	HistoryLimit          int
}
type Service struct {
	db        *sql.DB
	settings  *settings.Service
	startedAt time.Time
	effective MonitoringEffective
}
type Snapshot struct {
	Status      string `json:"status"`
	Application struct {
		Version          string    `json:"version,omitempty"`
		GoVersion        string    `json:"go_version"`
		ProcessStartedAt time.Time `json:"process_started_at"`
		UptimeSeconds    int64     `json:"uptime_seconds"`
	} `json:"application"`
	Platform struct {
		OS          string `json:"os"`
		Arch        string `json:"arch"`
		LogicalCPUs int    `json:"logical_cpus"`
	} `json:"platform"`
	Database struct {
		Type          string `json:"type"`
		SchemaVersion string `json:"schema_version,omitempty"`
		Healthy       bool   `json:"healthy"`
	} `json:"database"`
	Monitoring struct {
		SampleIntervalSeconds int  `json:"sample_interval_seconds"`
		HistoryLimit          int  `json:"history_limit"`
		RestartRequired       bool `json:"restart_required"`
	} `json:"monitoring"`
}

func New(db *sql.DB, settingService *settings.Service, effective MonitoringEffective, startedAt time.Time) *Service {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return &Service{db: db, settings: settingService, effective: effective, startedAt: startedAt}
}
func (s *Service) Get(ctx context.Context) Snapshot {
	var out Snapshot
	out.Status = "healthy"
	out.Application.GoVersion = runtime.Version()
	out.Application.ProcessStartedAt = s.startedAt.UTC()
	out.Application.UptimeSeconds = int64(time.Since(s.startedAt).Seconds())
	if out.Application.UptimeSeconds < 0 {
		out.Application.UptimeSeconds = 0
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		out.Application.Version = info.Main.Version
	}
	out.Platform.OS = runtime.GOOS
	out.Platform.Arch = runtime.GOARCH
	out.Platform.LogicalCPUs = runtime.NumCPU()
	out.Database.Type = "sqlite"
	out.Database.Healthy = true
	var version string
	if err := s.db.QueryRowContext(ctx, "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version); err != nil {
		out.Status = "degraded"
		out.Database.Healthy = false
	} else {
		out.Database.SchemaVersion = version
	}
	if s.settings != nil {
		if values, err := s.settings.Get(ctx); err == nil {
			out.Monitoring.SampleIntervalSeconds = values.Monitoring.SampleIntervalSeconds
			out.Monitoring.HistoryLimit = values.Monitoring.HistoryLimit
			out.Monitoring.RestartRequired = values.RestartRequired
		} else {
			out.Status = "degraded"
		}
	}
	if out.Monitoring.SampleIntervalSeconds == 0 {
		out.Monitoring.SampleIntervalSeconds = s.effective.SampleIntervalSeconds
	}
	if out.Monitoring.HistoryLimit == 0 {
		out.Monitoring.HistoryLimit = s.effective.HistoryLimit
	}
	return out
}
