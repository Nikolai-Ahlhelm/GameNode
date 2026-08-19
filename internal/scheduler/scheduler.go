package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/servers"
)

const (
	Daily  = "daily"
	Weekly = "weekly"
)

var (
	ErrInvalidSchedule = errors.New("invalid restart schedule")
	ErrNotFound        = sql.ErrNoRows
)

type Schedule struct {
	ID           string    `json:"id"`
	ServerID     string    `json:"server_id"`
	Enabled      bool      `json:"enabled"`
	ScheduleType string    `json:"schedule_type"`
	TimeOfDay    string    `json:"time_of_day"`
	DayOfWeek    *int      `json:"day_of_week,omitempty"`
	TimeZone     string    `json:"time_zone"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Patch struct {
	Enabled      *bool
	ScheduleType *string
	TimeOfDay    *string
	DayOfWeek    **int
	TimeZone     *string
}

func (s Schedule) Validate() error {
	if strings.TrimSpace(s.ServerID) == "" || len(s.ServerID) > 100 {
		return fmt.Errorf("%w: server id is required", ErrInvalidSchedule)
	}
	if s.ScheduleType != Daily && s.ScheduleType != Weekly {
		return fmt.Errorf("%w: schedule type must be daily or weekly", ErrInvalidSchedule)
	}
	if len(s.TimeOfDay) != 5 || s.TimeOfDay[2] != ':' {
		return fmt.Errorf("%w: time of day must use HH:MM", ErrInvalidSchedule)
	}
	hour, minute, err := parseClock(s.TimeOfDay)
	if err != nil || hour > 23 || minute > 59 {
		return fmt.Errorf("%w: time of day must use a valid 24-hour time", ErrInvalidSchedule)
	}
	if s.ScheduleType == Daily && s.DayOfWeek != nil {
		return fmt.Errorf("%w: daily schedules cannot specify a weekday", ErrInvalidSchedule)
	}
	if s.ScheduleType == Weekly && (s.DayOfWeek == nil || *s.DayOfWeek < 0 || *s.DayOfWeek > 6) {
		return fmt.Errorf("%w: weekly schedules require weekday 0 through 6", ErrInvalidSchedule)
	}
	if len(s.TimeZone) == 0 || len(s.TimeZone) > 100 || strings.ContainsRune(s.TimeZone, 0) {
		return fmt.Errorf("%w: timezone is required", ErrInvalidSchedule)
	}
	if _, err := time.LoadLocation(s.TimeZone); err != nil {
		return fmt.Errorf("%w: timezone is not a valid IANA timezone", ErrInvalidSchedule)
	}
	return nil
}

func parseClock(value string) (int, int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, errors.New("invalid clock")
	}
	if value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '9' {
		return 0, 0, errors.New("invalid clock")
	}
	return int(value[0]-'0')*10 + int(value[1]-'0'), int(value[3]-'0')*10 + int(value[4]-'0'), nil
}

// NextOccurrence returns the next strictly-future occurrence in the
// schedule's IANA timezone. A local wall-clock time that does not exist on a
// DST spring-forward date is skipped. time.Date's deterministic choice for
// an ambiguous fall-back time is used once; the scheduler's occurrence guard
// prevents a replacement timer from firing that same wall-clock occurrence a
// second time during the current process.
func NextOccurrence(now time.Time, schedule Schedule) (time.Time, error) {
	if err := schedule.Validate(); err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation(schedule.TimeZone)
	if err != nil {
		return time.Time{}, err
	}
	localNow := now.In(location)
	hour, minute, _ := parseClock(schedule.TimeOfDay)
	for offset := 0; offset <= 8; offset++ {
		day := localNow.AddDate(0, 0, offset)
		if schedule.ScheduleType == Weekly && int(day.Weekday()) != *schedule.DayOfWeek {
			continue
		}
		candidate := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, location)
		// Go normalizes nonexistent local times. Comparing the local fields
		// lets us skip that date instead of retrying at the normalized time.
		if candidate.In(location).Hour() != hour || candidate.In(location).Minute() != minute {
			continue
		}
		if candidate.After(now) {
			return candidate, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: could not calculate next occurrence", ErrInvalidSchedule)
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, schedule Schedule) (Schedule, error) {
	if err := schedule.Validate(); err != nil {
		return Schedule{}, err
	}
	if schedule.ID == "" {
		var err error
		schedule.ID, err = newID()
		if err != nil {
			return Schedule{}, err
		}
	}
	now := time.Now().UTC()
	schedule.CreatedAt, schedule.UpdatedAt = now, now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO server_restart_schedules(id,server_id,enabled,schedule_type,time_of_day,day_of_week,time_zone,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, schedule.ID, schedule.ServerID, schedule.Enabled, schedule.ScheduleType, schedule.TimeOfDay, nullableDay(schedule.DayOfWeek), schedule.TimeZone, stamp(now), stamp(now)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Schedule{}, ErrNotFound
		}
		return Schedule{}, err
	}
	return schedule, nil
}

func (s *Store) Get(ctx context.Context, id string) (Schedule, error) {
	return scanSchedule(s.db.QueryRowContext(ctx, `SELECT id,server_id,enabled,schedule_type,time_of_day,day_of_week,time_zone,created_at,updated_at FROM server_restart_schedules WHERE id=?`, id))
}

func (s *Store) ListServer(ctx context.Context, serverID string) ([]Schedule, error) {
	return s.list(ctx, `WHERE server_id=? ORDER BY time_of_day,id`, serverID)
}

func (s *Store) ListEnabled(ctx context.Context) ([]Schedule, error) {
	return s.list(ctx, `WHERE enabled=1 ORDER BY server_id,time_of_day,id`)
}

func (s *Store) list(ctx context.Context, suffix string, args ...any) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,server_id,enabled,schedule_type,time_of_day,day_of_week,time_zone,created_at,updated_at FROM server_restart_schedules `+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Schedule, 0)
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) Update(ctx context.Context, id string, patch Patch) (Schedule, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if patch.ScheduleType != nil {
		current.ScheduleType = strings.TrimSpace(*patch.ScheduleType)
	}
	if patch.TimeOfDay != nil {
		current.TimeOfDay = strings.TrimSpace(*patch.TimeOfDay)
	}
	if patch.DayOfWeek != nil {
		current.DayOfWeek = *patch.DayOfWeek
	}
	if patch.TimeZone != nil {
		current.TimeZone = strings.TrimSpace(*patch.TimeZone)
	}
	if err := current.Validate(); err != nil {
		return Schedule{}, err
	}
	current.UpdatedAt = time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `UPDATE server_restart_schedules SET enabled=?,schedule_type=?,time_of_day=?,day_of_week=?,time_zone=?,updated_at=? WHERE id=?`, current.Enabled, current.ScheduleType, current.TimeOfDay, nullableDay(current.DayOfWeek), current.TimeZone, stamp(current.UpdatedAt), id)
	if err != nil {
		return Schedule{}, err
	}
	return current, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM server_restart_schedules WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanSchedule(row scanner) (Schedule, error) {
	var schedule Schedule
	var enabled bool
	var day sql.NullInt64
	var created, updated string
	if err := row.Scan(&schedule.ID, &schedule.ServerID, &enabled, &schedule.ScheduleType, &schedule.TimeOfDay, &day, &schedule.TimeZone, &created, &updated); err != nil {
		return Schedule{}, err
	}
	schedule.Enabled = enabled
	if day.Valid {
		value := int(day.Int64)
		schedule.DayOfWeek = &value
	}
	schedule.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	schedule.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return schedule, nil
}

func nullableDay(day *int) any {
	if day == nil {
		return nil
	}
	return *day
}

func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

type RestartLifecycle interface {
	Restart(context.Context, string) (servers.Record, error)
}

type AuditSink interface {
	Record(context.Context, audit.Event) error
}

type Clock interface{ Now() time.Time }
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}
type TimerFactory interface{ NewTimer(time.Duration) Timer }
type realTimerFactory struct{}
type realTimer struct{ timer *time.Timer }

func (realTimerFactory) NewTimer(duration time.Duration) Timer {
	return &realTimer{timer: time.NewTimer(duration)}
}
func (t *realTimer) C() <-chan time.Time { return t.timer.C }
func (t *realTimer) Stop() bool          { return t.timer.Stop() }

type Scheduler struct {
	store     *Store
	lifecycle RestartLifecycle
	audit     AuditSink
	log       *slog.Logger
	clock     Clock
	timers    TimerFactory
	mu        sync.Mutex
	entries   map[string]*entry
	fired     map[string]map[string]struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

type entry struct {
	schedule   Schedule
	timer      Timer
	generation uint64
}

type Options struct {
	Audit  AuditSink
	Log    *slog.Logger
	Clock  Clock
	Timers TimerFactory
}

func New(store *Store, lifecycle RestartLifecycle, options Options) *Scheduler {
	logger := options.Log
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	clock := options.Clock
	if clock == nil {
		clock = realClock{}
	}
	timers := options.Timers
	if timers == nil {
		timers = realTimerFactory{}
	}
	return &Scheduler{store: store, lifecycle: lifecycle, audit: options.Audit, log: logger, clock: clock, timers: timers, entries: make(map[string]*entry), fired: make(map[string]map[string]struct{})}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return nil
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()
	schedules, err := s.store.ListEnabled(ctx)
	if err != nil {
		s.Stop()
		return err
	}
	for _, schedule := range schedules {
		if err := s.Replace(ctx, schedule); err != nil {
			s.log.Warn("restart schedule could not be registered", "module", "RestartScheduler", "schedule_id", schedule.ID, "error", err)
		}
	}
	s.log.Info("restart scheduler loaded schedules", "module", "RestartScheduler", "schedules", len(schedules))
	return nil
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.cancel == nil {
		s.mu.Unlock()
		return
	}
	s.cancel()
	for id, item := range s.entries {
		item.timer.Stop()
		delete(s.entries, id)
	}
	s.cancel = nil
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Scheduler) Replace(ctx context.Context, schedule Schedule) error {
	if err := schedule.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if old := s.entries[schedule.ID]; old != nil {
		old.timer.Stop()
		delete(s.entries, schedule.ID)
	}
	if !schedule.Enabled || s.cancel == nil {
		s.mu.Unlock()
		return nil
	}
	now := s.clock.Now()
	next, err := NextOccurrence(now, schedule)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	entry := &entry{schedule: schedule, generation: uint64(now.UnixNano())}
	entry.timer = s.timers.NewTimer(next.Sub(s.clock.Now()))
	s.entries[schedule.ID] = entry
	ctxRun := s.ctx
	s.wg.Add(1)
	s.mu.Unlock()
	go s.wait(ctxRun, schedule.ID, entry)
	s.log.Info("restart schedule registered", "module", "RestartScheduler", "schedule_id", schedule.ID, "server_id", schedule.ServerID, "next_restart", next.UTC().Format(time.RFC3339Nano))
	return nil
}

func (s *Scheduler) Remove(id string) {
	s.mu.Lock()
	if item := s.entries[id]; item != nil {
		item.timer.Stop()
		delete(s.entries, id)
	}
	s.mu.Unlock()
}

func (s *Scheduler) RemoveServer(serverID string) {
	s.mu.Lock()
	for id, item := range s.entries {
		if item.schedule.ServerID == serverID {
			item.timer.Stop()
			delete(s.entries, id)
		}
	}
	s.mu.Unlock()
}

func (s *Scheduler) wait(ctx context.Context, id string, item *entry) {
	defer s.wg.Done()
	select {
	case <-ctx.Done():
		return
	case firedAt := <-item.timer.C():
		s.fire(ctx, id, item, firedAt)
	}
}

func (s *Scheduler) fire(ctx context.Context, id string, item *entry, firedAt time.Time) {
	s.mu.Lock()
	current := s.entries[id]
	if current != item {
		s.mu.Unlock()
		return
	}
	delete(s.entries, id)
	key := occurrenceKey(item.schedule, firedAt)
	if seen := s.fired[id]; seen != nil {
		if _, ok := seen[key]; ok {
			s.mu.Unlock()
			s.scheduleNext(ctx, id)
			return
		}
	} else {
		s.fired[id] = make(map[string]struct{})
	}
	s.fired[id][key] = struct{}{}
	if len(s.fired[id]) > 128 {
		for oldKey := range s.fired[id] {
			delete(s.fired[id], oldKey)
			break
		}
	}
	s.mu.Unlock()

	currentSchedule, err := s.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.log.Info("scheduled restart skipped because schedule was deleted", "module", "RestartScheduler", "schedule_id", id)
		}
		return
	}
	if !currentSchedule.Enabled {
		s.log.Info("scheduled restart skipped because schedule is disabled", "module", "RestartScheduler", "schedule_id", id)
		return
	}
	s.log.Info("scheduled restart triggered", "module", "RestartScheduler", "schedule_id", id, "server_id", currentSchedule.ServerID)
	_, err = s.lifecycle.Restart(ctx, currentSchedule.ServerID)
	if err != nil {
		s.log.Warn("scheduled restart failed or server was not eligible", "module", "RestartScheduler", "schedule_id", id, "server_id", currentSchedule.ServerID, "error", err)
		if s.audit != nil {
			_ = s.audit.Record(context.Background(), audit.Event{Action: audit.ServerRestart, ResourceType: audit.Server, ResourceID: &currentSchedule.ServerID, ServerID: &currentSchedule.ServerID, Result: audit.Failure, ErrorCode: "scheduled_restart_failed", ErrorSummary: "scheduled restart was not completed", Metadata: []byte(fmt.Sprintf(`{"origin":"scheduled","schedule_id":%q}`, id))})
		}
	} else {
		if s.audit != nil {
			_ = s.audit.Record(context.Background(), audit.Event{Action: audit.ServerRestart, ResourceType: audit.Server, ResourceID: &currentSchedule.ServerID, ServerID: &currentSchedule.ServerID, Result: audit.Success, Metadata: []byte(fmt.Sprintf(`{"origin":"scheduled","schedule_id":%q}`, id))})
		}
	}
	if currentSchedule.Enabled {
		_ = s.Replace(ctx, currentSchedule)
	}
}

func (s *Scheduler) scheduleNext(ctx context.Context, id string) {
	schedule, err := s.store.Get(ctx, id)
	if err != nil || !schedule.Enabled {
		return
	}
	if err := s.Replace(ctx, schedule); err != nil {
		s.log.Warn("restart schedule could not be re-registered", "module", "RestartScheduler", "schedule_id", id, "error", err)
	}
}

func occurrenceKey(schedule Schedule, value time.Time) string {
	local := value.In(mustLocation(schedule.TimeZone))
	return fmt.Sprintf("%s|%s|%02d:%02d", schedule.ID, local.Format("2006-01-02"), local.Hour(), local.Minute())
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}
