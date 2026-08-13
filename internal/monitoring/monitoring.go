// Package monitoring samples identity-verified native-process metrics. It is
// transport-independent and keeps only a bounded in-memory history.
package monitoring

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"gamenode/internal/runtime"
)

const (
	HealthUnknown  = "unknown"
	HealthHealthy  = "healthy"
	HealthDegraded = "degraded"
	HealthStopped  = "stopped"
	HealthCrashed  = "crashed"
	HealthDetached = "detached"
)

type Options struct {
	Interval     time.Duration
	HistoryLimit int
}
type Sample struct {
	Timestamp   time.Time `json:"timestamp"`
	CPUPercent  float64   `json:"cpu_percent"`
	MemoryBytes uint64    `json:"memory_bytes"`
	ThreadCount uint32    `json:"thread_count,omitempty"`
	HandleCount uint32    `json:"handle_count,omitempty"`
}
type Snapshot struct {
	Health              string     `json:"health"`
	State               string     `json:"state"`
	PID                 int        `json:"pid,omitempty"`
	UptimeSeconds       int64      `json:"uptime_seconds"`
	CPUPercent          float64    `json:"cpu_percent"`
	MemoryBytes         uint64     `json:"memory_bytes"`
	ThreadCount         uint32     `json:"thread_count,omitempty"`
	HandleCount         uint32     `json:"handle_count,omitempty"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	LastExitAt          *time.Time `json:"last_exit_at,omitempty"`
	LastExitCode        *int       `json:"last_exit_code,omitempty"`
	CrashCount          int        `json:"crash_count"`
	RestartCount        int        `json:"restart_count"`
	LastError           string     `json:"last_error,omitempty"`
	AutoRestartEnabled  bool       `json:"auto_restart_enabled"`
	PendingAutoRestart  bool       `json:"pending_auto_restart"`
	AutoRestartAttempts int        `json:"auto_restart_attempts"`
	RestartLimitReached bool       `json:"restart_limit_reached"`
}
type Input struct {
	ServerID, State                        string
	PID                                    int
	Identity                               runtime.Identity
	StartedAt, LastExitAt                  *time.Time
	LastExitCode                           *int
	CrashCount, RestartCount               int
	LastError                              string
	Detached                               bool
	AutoRestartEnabled, PendingAutoRestart bool
	AutoRestartAttempts                    int
	RestartLimitReached                    bool
}
type tracked struct {
	identity         runtime.Identity
	detached         bool
	previous         runtime.Metrics
	previousAt       time.Time
	samples          []Sample
	metricsAvailable bool
	active           bool
	metricsFailed    bool
}
type Service struct {
	runtime  runtime.Runtime
	interval time.Duration
	limit    int
	mu       sync.RWMutex
	tracked  map[string]*tracked
	log      *slog.Logger
}

func New(r runtime.Runtime, options Options) *Service {
	if options.Interval <= 0 {
		options.Interval = 5 * time.Second
	}
	if options.HistoryLimit <= 0 {
		options.HistoryLimit = 300
	}
	s := &Service{runtime: r, interval: options.Interval, limit: options.HistoryLimit, tracked: make(map[string]*tracked), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go s.loop()
	return s
}

// SetLogger enables background sampler diagnostics without changing sampling behavior.
func (s *Service) SetLogger(log *slog.Logger) {
	if log != nil {
		s.mu.Lock()
		s.log = log
		s.mu.Unlock()
	}
}
func (s *Service) loop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for range ticker.C {
		s.Sample(context.Background())
	}
}
func (s *Service) ObserveRunning(server string, identity runtime.Identity, detached bool) {
	if identity.PID <= 0 || identity.StartKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.tracked[server]
	if !ok || current.identity != identity {
		var samples []Sample
		if current != nil {
			samples = current.samples
		}
		s.tracked[server] = &tracked{identity: identity, detached: detached, samples: samples, active: true}
		s.log.Info("process monitoring started", "module", "Monitoring", "server_id", server, "pid", identity.PID, "detached", detached)
		return
	}
	current.detached = detached
	current.active = true
}
func (s *Service) ObserveExit(server string, identity runtime.Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.tracked[server]; ok && current.identity == identity {
		current.active = false
		s.log.Info("process monitoring stopped", "module", "Monitoring", "server_id", server, "pid", identity.PID)
	}
}

// Sample is safe to call in tests and runs the normal identity-safe collector.
func (s *Service) Sample(ctx context.Context) {
	s.mu.RLock()
	targets := make(map[string]runtime.Identity, len(s.tracked))
	for id, item := range s.tracked {
		if item.active {
			targets[id] = item.identity
		}
	}
	s.mu.RUnlock()
	for id, identity := range targets {
		s.sampleOne(ctx, id, identity)
	}
}
func (s *Service) sampleOne(ctx context.Context, server string, identity runtime.Identity) {
	metrics, err := s.runtime.Metrics(ctx, identity)
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.tracked[server]
	if !ok || item.identity != identity {
		return
	} // stale sample cannot affect a newer process
	if err != nil {
		item.metricsAvailable = false
		if !item.metricsFailed {
			item.metricsFailed = true
			s.log.Warn("process metrics sampling failed", "module", "Monitoring", "server_id", server, "pid", identity.PID, "error", err)
		}
		return
	}
	if item.metricsFailed {
		s.log.Info("process metrics sampling recovered", "module", "Monitoring", "server_id", server, "pid", identity.PID)
		item.metricsFailed = false
	}
	cpu := 0.0
	if !item.previousAt.IsZero() {
		elapsed := now.Sub(item.previousAt)
		if elapsed > 0 {
			cpu = float64(metrics.CPUTime-item.previous.CPUTime) / float64(elapsed) * 100
		}
	}
	item.previous, item.previousAt, item.metricsAvailable = metrics, now, true
	item.samples = append(item.samples, Sample{Timestamp: now, CPUPercent: cpu, MemoryBytes: metrics.MemoryBytes, ThreadCount: metrics.ThreadCount, HandleCount: metrics.HandleCount})
	if extra := len(item.samples) - s.limit; extra > 0 {
		copy(item.samples, item.samples[extra:])
		item.samples = item.samples[:s.limit]
	}
}
func (s *Service) Current(in Input) Snapshot {
	result := Snapshot{State: in.State, PID: in.PID, StartedAt: in.StartedAt, LastExitAt: in.LastExitAt, LastExitCode: in.LastExitCode, CrashCount: in.CrashCount, RestartCount: in.RestartCount, LastError: in.LastError, Health: health(in), AutoRestartEnabled: in.AutoRestartEnabled, PendingAutoRestart: in.PendingAutoRestart, AutoRestartAttempts: in.AutoRestartAttempts, RestartLimitReached: in.RestartLimitReached}
	if in.StartedAt != nil && (in.State == "running" || in.State == "starting" || in.State == "stopping") {
		result.UptimeSeconds = int64(time.Since(*in.StartedAt).Seconds())
		if result.UptimeSeconds < 0 {
			result.UptimeSeconds = 0
		}
	}
	s.mu.RLock()
	item := s.trackedFor(in)
	if item != nil && len(item.samples) > 0 {
		sample := item.samples[len(item.samples)-1]
		result.CPUPercent, result.MemoryBytes, result.ThreadCount, result.HandleCount = sample.CPUPercent, sample.MemoryBytes, sample.ThreadCount, sample.HandleCount
	}
	available := item != nil && item.metricsAvailable
	s.mu.RUnlock()
	if in.State == "running" && !in.Detached && !available {
		result.Health = HealthDegraded
	}
	return result
}
func (s *Service) History(server string) []Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item := s.tracked[server]
	if item == nil {
		return []Sample{}
	}
	return append([]Sample(nil), item.samples...)
}
func (s *Service) trackedFor(in Input) *tracked {
	item := s.tracked[in.ServerID]
	if item == nil || item.identity != in.Identity {
		return nil
	}
	return item
}
func health(in Input) string {
	if in.Detached {
		return HealthDetached
	}
	switch in.State {
	case "running":
		return HealthHealthy
	case "stopped":
		return HealthStopped
	case "crashed":
		return HealthCrashed
	default:
		return HealthUnknown
	}
}
