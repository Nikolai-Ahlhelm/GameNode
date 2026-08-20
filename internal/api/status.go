package api

import (
	"net/http"
	"strings"
	"time"

	"gamenode/internal/rbac"
	"gamenode/internal/statushistory"
	"gamenode/internal/tenants"
)

type statusService struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	State        string   `json:"state"`
	Health       string   `json:"health"`
	Availability float64  `json:"availability_percent"`
	History      []string `json:"history"`
}

// statusPageHandler is the only API surface that may expose tenant runtime
// state without a session. The tenant must explicitly enable the page and
// mark it public; private pages require tenant-scoped Monitoring.View.
func (s *Server) statusPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	slug := ""
	if r.URL.Path != "/api/v1/status" {
		slug = strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/status/"), "/")
	}
	if slug == "" {
		slug = tenants.DefaultTenantID
	}
	tenant, err := s.tenants.GetBySlug(r.Context(), slug)
	if err != nil || !tenant.StatusPageEnabled {
		notFound(w)
		return
	}
	if !tenant.StatusPagePublic {
		if _, _, ok := s.requirePermission(w, r, "Monitoring.View", rbac.Scope{Type: "tenant", ID: &tenant.ID}, false); !ok {
			return
		}
	}
	records, err := s.servers.List(r.Context())
	if err != nil {
		internal(w)
		return
	}
	services := make([]statusService, 0)
	for _, record := range records {
		if record.Server.TenantID != tenant.ID {
			continue
		}
		snapshot, snapshotErr := s.servers.MonitoringSnapshot(r.Context(), record.Server.ID)
		health := "unknown"
		if snapshotErr == nil {
			health = snapshot.Health
		}
		current := statusPoint(health)
		checks := []statushistory.Check(nil)
		if s.statusHistory != nil {
			checks, _ = s.statusHistory.List(r.Context(), record.Server.ID, time.Now().UTC().Add(-statushistory.Retention))
		}
		points, availability := statusHistoryPoints(checks, current, time.Now().UTC())
		services = append(services, statusService{ID: record.Server.ID, Name: record.Server.Name, State: record.Runtime.CurrentState, Health: health, Availability: availability, History: points})
	}
	jsonOut(w, http.StatusOK, map[string]any{
		"tenant": map[string]any{"name": tenant.Name, "slug": tenant.Slug},
		"public": tenant.StatusPagePublic, "updated_at": time.Now().UTC(), "services": services,
	})
}

func statusPoint(health string) string {
	return statushistory.StatusFromHealth(health)
}

// statusHistoryPoints compresses up to 30 days of five-minute checks into 90
// visual buckets. A bucket is unknown when there was no check; it is never
// silently treated as uptime. The availability percentage is calculated from
// the actual persisted checks, not from the compressed visual representation.
func statusHistoryPoints(checks []statushistory.Check, current string, now time.Time) ([]string, float64) {
	const buckets = 90
	if len(checks) == 0 {
		return []string{current}, map[string]float64{"up": 100, "degraded": 0, "down": 0}[current]
	}
	points := make([]string, buckets)
	for i := range points {
		points[i] = "unknown"
	}
	since := now.Add(-statushistory.Retention)
	width := statushistory.Retention / buckets
	up := 0
	for _, check := range checks {
		if check.Status == "up" {
			up++
		}
		index := int(check.CheckedAt.Sub(since) / width)
		if index < 0 {
			continue
		}
		if index >= buckets {
			index = buckets - 1
		}
		if points[index] == "unknown" || statusSeverity(check.Status) > statusSeverity(points[index]) {
			points[index] = check.Status
		}
	}
	// Keep the current snapshot visible even before the next five-minute
	// recorder tick. This does not alter the persisted availability calculation.
	if points[buckets-1] == "unknown" {
		points[buckets-1] = current
	}
	return points, float64(up) / float64(len(checks)) * 100
}

func statusSeverity(status string) int {
	switch status {
	case "down":
		return 3
	case "degraded":
		return 2
	case "up":
		return 1
	default:
		return 0
	}
}
