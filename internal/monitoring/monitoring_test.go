package monitoring

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"gamenode/internal/runtime"
)

type fakeRuntime struct {
	mu      sync.Mutex
	metrics runtime.Metrics
	err     error
}

func (f *fakeRuntime) Start(context.Context, runtime.StartOptions) (runtime.Identity, <-chan runtime.ExitResult, error) {
	return runtime.Identity{}, nil, errors.New("unused")
}
func (f *fakeRuntime) Stop(context.Context, runtime.Identity, time.Duration) error {
	return errors.New("unused")
}
func (f *fakeRuntime) Kill(context.Context, runtime.Identity) error { return errors.New("unused") }
func (f *fakeRuntime) Status(context.Context, runtime.Identity) (runtime.Status, error) {
	return runtime.Status{}, errors.New("unused")
}
func (f *fakeRuntime) Metrics(context.Context, runtime.Identity) (runtime.Metrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.metrics, f.err
}

func input(identity runtime.Identity, state string) Input {
	started := time.Now().Add(-2 * time.Minute)
	return Input{ServerID: "server", State: state, PID: identity.PID, Identity: identity, StartedAt: &started}
}
func TestHealthAndBoundedSamples(t *testing.T) {
	fake := &fakeRuntime{metrics: runtime.Metrics{MemoryBytes: 42, CPUTime: time.Second}}
	service := New(fake, Options{Interval: time.Hour, HistoryLimit: 2})
	identity := runtime.Identity{PID: 7, StartKey: "one"}
	if got := service.Current(input(identity, "stopped")).Health; got != HealthStopped {
		t.Fatalf("stopped health = %s", got)
	}
	service.ObserveRunning("server", identity, false)
	service.Sample(context.Background())
	if got := service.Current(input(identity, "running")); got.Health != HealthHealthy || got.MemoryBytes != 42 || got.UptimeSeconds < 119 {
		t.Fatalf("running snapshot = %#v", got)
	}
	service.Sample(context.Background())
	service.Sample(context.Background())
	if got := len(service.History("server")); got != 2 {
		t.Fatalf("history length = %d", got)
	}
	fake.err = errors.New("metrics unavailable")
	service.Sample(context.Background())
	if got := service.Current(input(identity, "running")).Health; got != HealthDegraded {
		t.Fatalf("metrics failure health = %s", got)
	}
}
func TestStaleIdentityCannotOverwriteNewerProcess(t *testing.T) {
	fake := &fakeRuntime{metrics: runtime.Metrics{MemoryBytes: 10}}
	service := New(fake, Options{Interval: time.Hour, HistoryLimit: 2})
	old := runtime.Identity{PID: 1, StartKey: "old"}
	newer := runtime.Identity{PID: 2, StartKey: "new"}
	service.ObserveRunning("server", old, false)
	service.ObserveRunning("server", newer, false)
	service.ObserveExit("server", old)
	service.Sample(context.Background())
	if got := service.Current(input(newer, "running")); got.Health != HealthHealthy || got.PID != 2 {
		t.Fatalf("new process was overwritten: %#v", got)
	}
}

func TestMetricsFailureAndRecoveryAreLoggedOncePerTransition(t *testing.T) {
	fake := &fakeRuntime{err: errors.New("metrics unavailable")}
	service := New(fake, Options{Interval: time.Hour, HistoryLimit: 2})
	var logs bytes.Buffer
	service.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	identity := runtime.Identity{PID: 7, StartKey: "one"}
	service.ObserveRunning("server", identity, false)
	service.Sample(context.Background())
	service.Sample(context.Background())
	if got := strings.Count(logs.String(), "process metrics sampling failed"); got != 1 {
		t.Fatalf("failure logs=%d: %s", got, logs.String())
	}
	fake.mu.Lock()
	fake.err = nil
	fake.mu.Unlock()
	service.Sample(context.Background())
	if !strings.Contains(logs.String(), "process metrics sampling recovered") {
		t.Fatalf("missing recovery log: %s", logs.String())
	}
}
