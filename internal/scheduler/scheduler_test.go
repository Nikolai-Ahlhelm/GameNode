package scheduler

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/audit"
	"gamenode/internal/database"
	"gamenode/internal/servers"
)

func TestNextOccurrenceDailyWeeklyAndTimezone(t *testing.T) {
	berlin := mustTime(t, "2026-08-19T01:00:00Z")
	daily := Schedule{ServerID: "server", Enabled: true, ScheduleType: Daily, TimeOfDay: "04:00", TimeZone: "Europe/Berlin"}
	next, err := NextOccurrence(berlin, daily)
	if err != nil || next.Format(time.RFC3339) != "2026-08-19T04:00:00+02:00" {
		t.Fatalf("daily next=%v err=%v", next, err)
	}
	if next, err = NextOccurrence(mustTime(t, "2026-08-19T05:00:00+02:00"), daily); err != nil || next.Day() != 20 {
		t.Fatalf("daily after=%v err=%v", next, err)
	}
	day := int(time.Sunday)
	weekly := Schedule{ServerID: "server", Enabled: true, ScheduleType: Weekly, TimeOfDay: "04:00", DayOfWeek: &day, TimeZone: "Europe/Berlin"}
	if next, err = NextOccurrence(mustTime(t, "2026-08-19T02:00:00Z"), weekly); err != nil || next.Weekday() != time.Sunday {
		t.Fatalf("weekly rollover=%v err=%v", next, err)
	}
	if next, err = NextOccurrence(mustTime(t, "2026-08-23T05:00:00+02:00"), weekly); err != nil || next.Day() != 30 {
		t.Fatalf("weekly same-day after=%v err=%v", next, err)
	}
}

func TestNextOccurrenceSkipsNonexistentDSTTime(t *testing.T) {
	daily := Schedule{ServerID: "server", Enabled: true, ScheduleType: Daily, TimeOfDay: "02:30", TimeZone: "Europe/Berlin"}
	next, err := NextOccurrence(mustTime(t, "2026-03-29T00:00:00Z"), daily)
	if err != nil {
		t.Fatal(err)
	}
	if next.In(mustLocation("Europe/Berlin")).Day() != 30 || next.In(mustLocation("Europe/Berlin")).Hour() != 2 {
		t.Fatalf("nonexistent local time was not skipped: %v", next)
	}
}

func TestSchedulerReloadDisableDeleteAndOnce(t *testing.T) {
	db, store, serverID := scheduleFixture(t)
	clock := &fakeClock{now: mustTime(t, "2026-08-19T00:00:00Z")}
	timers := &fakeTimers{}
	lifecycle := &fakeLifecycle{}
	sink := &fakeAudit{}
	s := New(store, lifecycle, Options{Clock: clock, Timers: timers, Audit: sink})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	schedule, err := store.Create(context.Background(), Schedule{ServerID: serverID, Enabled: true, ScheduleType: Daily, TimeOfDay: "04:00", TimeZone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	old := timers.latest()
	old.fire(clock.now.Add(4 * time.Hour))
	lifecycle.waitFor(t, 1)
	old.fire(clock.now.Add(4 * time.Hour))
	time.Sleep(5 * time.Millisecond)
	if lifecycle.count() != 1 {
		t.Fatalf("restart count=%d, want once", lifecycle.count())
	}

	disabled := false
	if _, err = store.Update(context.Background(), schedule.ID, Patch{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.Get(context.Background(), schedule.ID)
	if err = s.Replace(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	latest := timers.latest()
	latest.fire(clock.now.Add(24 * time.Hour))
	time.Sleep(5 * time.Millisecond)
	if lifecycle.count() != 1 {
		t.Fatalf("disabled schedule restarted: %d", lifecycle.count())
	}
	if err = store.Delete(context.Background(), schedule.ID); err != nil {
		t.Fatal(err)
	}
	s.Remove(schedule.ID)
	s.Stop()
	_ = db.Close()
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	result, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakeTimer struct {
	ch      chan time.Time
	stopped bool
}

func (t *fakeTimer) C() <-chan time.Time  { return t.ch }
func (t *fakeTimer) Stop() bool           { t.stopped = true; return true }
func (t *fakeTimer) fire(value time.Time) { t.ch <- value }

type fakeTimers struct {
	mu     sync.Mutex
	values []*fakeTimer
}

func (f *fakeTimers) NewTimer(time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	timer := &fakeTimer{ch: make(chan time.Time, 1)}
	f.values = append(f.values, timer)
	return timer
}
func (f *fakeTimers) latest() *fakeTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[len(f.values)-1]
}

type fakeLifecycle struct {
	mu     sync.Mutex
	calls  []string
	called chan struct{}
}

func (f *fakeLifecycle) Restart(_ context.Context, id string) (servers.Record, error) {
	f.mu.Lock()
	f.calls = append(f.calls, id)
	if f.called != nil {
		close(f.called)
		f.called = nil
	}
	f.mu.Unlock()
	return servers.Record{}, nil
}
func (f *fakeLifecycle) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }
func (f *fakeLifecycle) waitFor(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f.count() >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("lifecycle calls=%d, want %d", f.count(), count)
}

type fakeAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeAudit) Record(_ context.Context, event audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func scheduleFixture(t *testing.T) (*sql.DB, *Store, string) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	executable := filepath.Join(root, "server.exe")
	if err = os.WriteFile(executable, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	service := servers.NewStore(db)
	record, err := service.Create(context.Background(), servers.Server{TenantID: "default", Name: "Scheduled", WorkingDirectory: root, Executable: "server.exe"})
	if err != nil {
		t.Fatal(err)
	}
	return db, NewStore(db), record.Server.ID
}
