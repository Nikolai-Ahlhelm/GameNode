// Package support creates a bounded, sanitized support ZIP through an io.Writer.
package support

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/diagnostics"
	"gamenode/internal/monitoring"
	"gamenode/internal/servers"
	"gamenode/internal/settings"
)

const AuditLimit = 100
const MaxBundleBytes int64 = 10 << 20

var ErrBundleTooLarge = errors.New("support bundle exceeds size limit")

type Scope struct{}
type Service struct {
	diagnostics diagnosticsSource
	settings    settingsSource
	audit       auditSource
	servers     serversSource
}

type diagnosticsSource interface {
	Get(context.Context) diagnostics.Snapshot
}
type settingsSource interface {
	Get(context.Context) (settings.Values, error)
}
type auditSource interface {
	List(context.Context, audit.Filter) ([]audit.Event, error)
}
type serversSource interface {
	List(context.Context) ([]servers.Record, error)
	MonitoringSnapshot(context.Context, string) (monitoring.Snapshot, error)
}

func New(d diagnosticsSource, st settingsSource, a auditSource, srv serversSource) *Service {
	return &Service{d, st, a, srv}
}

type manifest struct {
	BundleSchemaVersion int       `json:"bundle_schema_version"`
	GeneratedAt         time.Time `json:"generated_at"`
	Format              string    `json:"format"`
	Warnings            []string  `json:"warnings,omitempty"`
}
type serverSummary struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	State              string `json:"state"`
	Health             string `json:"health"`
	AutoRestartEnabled bool   `json:"auto_restart_enabled"`
	CrashCount         int    `json:"crash_count"`
	RestartCount       int    `json:"restart_count"`
}

// supportSettings is deliberately narrower than settings.Values. Support
// bundles have an explicit data whitelist; adding a new persisted setting
// must not silently expand the bundle before it is reviewed.
type supportSettings struct {
	Monitoring            settings.Monitoring `json:"monitoring"`
	Logging               settings.Logging    `json:"logging"`
	Security              settings.Security   `json:"security"`
	Branding              settings.Branding   `json:"branding"`
	RestartRequired       bool                `json:"restart_required"`
	RestartRequiredFields []string            `json:"restart_required_fields"`
}

func sanitizeSettings(values settings.Values) supportSettings {
	return supportSettings{
		Monitoring:            values.Monitoring,
		Logging:               values.Logging,
		Security:              values.Security,
		Branding:              values.Branding,
		RestartRequired:       values.RestartRequired,
		RestartRequiredFields: append([]string(nil), values.RestartRequiredFields...),
	}
}

func (s *Service) Generate(ctx context.Context, w io.Writer, _ Scope) error {
	w = &limitWriter{w: w, remaining: MaxBundleBytes}
	diagnostic := s.diagnostics.Get(ctx)
	values, err := s.settings.Get(ctx)
	if err != nil {
		return err
	}
	warnings := []string{}
	events, err := s.audit.List(ctx, audit.Filter{Limit: AuditLimit})
	if err != nil {
		events = []audit.Event{}
		warnings = append(warnings, "recent audit events unavailable")
	}
	records, err := s.servers.List(ctx)
	if err != nil {
		return err
	}
	summaries := make([]serverSummary, 0, len(records))
	for _, r := range records {
		m, e := s.servers.MonitoringSnapshot(ctx, r.Server.ID)
		if e != nil {
			m.Health = "unknown"
		}
		summaries = append(summaries, serverSummary{r.Server.ID, r.Server.Name, r.Runtime.CurrentState, m.Health, r.Server.AutoRestartEnabled, r.Runtime.CrashCount, r.Runtime.RestartCount})
	}
	z := zip.NewWriter(w)
	defer z.Close()
	entries := []struct {
		name  string
		value any
	}{{"manifest.json", manifest{1, time.Now().UTC(), "zip", warnings}}, {"diagnostics.json", diagnostic}, {"settings.json", sanitizeSettings(values)}, {"audit-recent.json", events}, {"servers.json", summaries}}
	for _, e := range entries {
		f, err := z.Create(e.name)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(e.value); err != nil {
			return err
		}
	}
	return z.Close()
}

type limitWriter struct {
	w         io.Writer
	remaining int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, ErrBundleTooLarge
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}
