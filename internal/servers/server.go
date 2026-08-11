package servers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gamenode/internal/console"
	"gamenode/internal/monitoring"
	"gamenode/internal/ports"
	"gamenode/internal/runtime"
)

const (
	CreationNew    = "new"
	CreationAdopt  = "adopt"
	CreationCustom = "custom"
	StateStopped   = "stopped"
	StateRunning   = "running"
	StateStarting  = "starting"
	StateStopping  = "stopping"
	StateCrashed   = "crashed"
	StateUnknown   = "unknown"
)

var environmentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Server struct {
	ID                       string            `json:"id"`
	CreationMode             string            `json:"creation_mode"`
	Name                     string            `json:"name"`
	Description              string            `json:"description"`
	WorkingDirectory         string            `json:"working_directory"`
	Executable               string            `json:"executable"`
	Arguments                []string          `json:"arguments"`
	EnvironmentVariables     map[string]string `json:"environment_variables"`
	RuntimeType              string            `json:"runtime_type"`
	AutoStart                bool              `json:"auto_start"`
	RestartPolicy            string            `json:"restart_policy"`
	StopMethod               string            `json:"stop_method"`
	StopCommand              string            `json:"stop_command"`
	StopTimeoutSeconds       int               `json:"stop_timeout_seconds"`
	AutoRestartEnabled       bool              `json:"auto_restart_enabled"`
	AutoRestartMaxAttempts   int               `json:"auto_restart_max_attempts"`
	AutoRestartWindowSeconds int               `json:"auto_restart_window_seconds"`
	AutoRestartDelaySeconds  int               `json:"auto_restart_delay_seconds"`
	CreatedAt                time.Time         `json:"created_at"`
	UpdatedAt                time.Time         `json:"updated_at"`
}

type RuntimeState struct {
	PID             int        `json:"pid,omitempty"`
	ProcessStartAt  *time.Time `json:"process_started_at,omitempty"`
	LastStartAt     *time.Time `json:"last_start_at,omitempty"`
	LastStopAt      *time.Time `json:"last_stop_at,omitempty"`
	LastExitAt      *time.Time `json:"last_exit_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	LastCrashAt     *time.Time `json:"last_crash_at,omitempty"`
	CrashCount      int        `json:"crash_count"`
	RestartCount    int        `json:"restart_count"`
	LastError       string     `json:"last_error,omitempty"`
	CurrentState    string     `json:"current_state"`
	processStartKey string
}

type Record struct {
	Server  Server       `json:"server"`
	Runtime RuntimeState `json:"runtime"`
}
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Server) Validate() error {
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.TrimSpace(s.Description)
	s.WorkingDirectory = filepath.Clean(strings.TrimSpace(s.WorkingDirectory))
	s.Executable = strings.TrimSpace(s.Executable)
	if len(s.Name) < 1 || len(s.Name) > 100 {
		return errors.New("name must be 1 to 100 characters")
	}
	if s.CreationMode == "" {
		s.CreationMode = CreationCustom
	}
	if s.CreationMode != CreationNew && s.CreationMode != CreationAdopt && s.CreationMode != CreationCustom {
		return errors.New("invalid creation mode")
	}
	if !filepath.IsAbs(s.WorkingDirectory) {
		return errors.New("working directory must be an absolute path")
	}
	info, err := os.Stat(s.WorkingDirectory)
	if err != nil || !info.IsDir() {
		return errors.New("working directory must exist")
	}
	if s.Executable == "" || strings.ContainsRune(s.Executable, 0) {
		return errors.New("executable is required")
	}
	resolved := s.ResolvedExecutable()
	if !filepath.IsAbs(s.Executable) && !inside(s.WorkingDirectory, resolved) {
		return errors.New("relative executable escapes working directory")
	}
	executableInfo, err := os.Stat(resolved)
	if err != nil || executableInfo.IsDir() {
		return errors.New("executable must be an existing file")
	}
	if s.RuntimeType == "" {
		s.RuntimeType = "native"
	}
	if s.RuntimeType != "native" {
		return errors.New("only native runtime is supported")
	}
	if s.RestartPolicy == "" {
		s.RestartPolicy = "never"
	}
	if s.RestartPolicy != "never" {
		return errors.New("restart policies are not available in Milestone 2")
	}
	if s.StopMethod == "" {
		s.StopMethod = "terminate"
	}
	if s.StopMethod != "terminate" {
		return errors.New("only terminate stop method is supported")
	}
	if s.StopCommand != "" {
		return errors.New("stop command is not available without console support")
	}
	if s.StopTimeoutSeconds == 0 {
		s.StopTimeoutSeconds = 15
	}
	if s.StopTimeoutSeconds < 1 || s.StopTimeoutSeconds > 300 {
		return errors.New("stop timeout must be between 1 and 300 seconds")
	}
	if s.AutoRestartMaxAttempts == 0 {
		s.AutoRestartMaxAttempts = 3
	}
	if s.AutoRestartWindowSeconds == 0 {
		s.AutoRestartWindowSeconds = 300
	}
	if !s.AutoRestartEnabled && s.AutoRestartDelaySeconds == 0 {
		s.AutoRestartDelaySeconds = 5
	}
	if s.AutoRestartMaxAttempts < 1 || s.AutoRestartMaxAttempts > 20 {
		return errors.New("auto restart max attempts must be between 1 and 20")
	}
	if s.AutoRestartWindowSeconds < 1 || s.AutoRestartWindowSeconds > 86400 {
		return errors.New("auto restart window must be between 1 and 86400 seconds")
	}
	if s.AutoRestartDelaySeconds < 0 || s.AutoRestartDelaySeconds > 3600 {
		return errors.New("auto restart delay must be between 0 and 3600 seconds")
	}
	if len(s.Arguments) > 128 {
		return errors.New("too many arguments")
	}
	for _, argument := range s.Arguments {
		if len(argument) > 4096 || strings.ContainsRune(argument, 0) {
			return errors.New("invalid argument")
		}
	}
	if len(s.EnvironmentVariables) > 64 {
		return errors.New("too many environment variables")
	}
	for key, value := range s.EnvironmentVariables {
		if !environmentKey.MatchString(key) || len(value) > 8192 || strings.ContainsRune(value, 0) {
			return errors.New("invalid environment variable")
		}
	}
	return nil
}
func (s Server) ResolvedExecutable() string {
	if filepath.IsAbs(s.Executable) {
		return filepath.Clean(s.Executable)
	}
	return filepath.Join(s.WorkingDirectory, s.Executable)
}
func inside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (store *Store) Create(ctx context.Context, server Server) (Record, error) {
	if err := server.Validate(); err != nil {
		return Record{}, err
	}
	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	server.ID = id
	server.CreatedAt = now
	server.UpdatedAt = now
	args, _ := json.Marshal(server.Arguments)
	env, _ := json.Marshal(server.EnvironmentVariables)
	_, err = store.db.ExecContext(ctx, `INSERT INTO servers(id,creation_mode,name,description,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,auto_restart_enabled,auto_restart_max_attempts,auto_restart_window_seconds,auto_restart_delay_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, server.ID, server.CreationMode, server.Name, server.Description, server.WorkingDirectory, server.Executable, string(args), string(env), server.RuntimeType, server.AutoStart, server.RestartPolicy, server.StopMethod, server.StopCommand, server.StopTimeoutSeconds, server.AutoRestartEnabled, server.AutoRestartMaxAttempts, server.AutoRestartWindowSeconds, server.AutoRestartDelaySeconds, stamp(now), stamp(now))
	if err != nil {
		return Record{}, fmt.Errorf("create server: %w", err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO server_runtime_state(server_id,current_state,updated_at) VALUES(?,?,?)`, server.ID, StateStopped, stamp(now))
	if err != nil {
		return Record{}, err
	}
	return Record{Server: server, Runtime: RuntimeState{CurrentState: StateStopped}}, nil
}
func (store *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := store.db.QueryContext(ctx, selectSQL+` ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		record, err := scan(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
func (store *Store) Get(ctx context.Context, id string) (Record, error) {
	row := store.db.QueryRowContext(ctx, selectSQL+` WHERE s.id=?`, id)
	return scan(row)
}
func (store *Store) Update(ctx context.Context, id string, server Server) (Record, error) {
	existing, err := store.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	server.ID = id
	server.CreatedAt = existing.Server.CreatedAt
	if err = server.Validate(); err != nil {
		return Record{}, err
	}
	server.UpdatedAt = time.Now().UTC()
	args, _ := json.Marshal(server.Arguments)
	env, _ := json.Marshal(server.EnvironmentVariables)
	_, err = store.db.ExecContext(ctx, `UPDATE servers SET creation_mode=?,name=?,description=?,working_directory=?,executable=?,arguments_json=?,environment_json=?,runtime_type=?,auto_start=?,restart_policy=?,stop_method=?,stop_command=?,stop_timeout_seconds=?,auto_restart_enabled=?,auto_restart_max_attempts=?,auto_restart_window_seconds=?,auto_restart_delay_seconds=?,updated_at=? WHERE id=?`, server.CreationMode, server.Name, server.Description, server.WorkingDirectory, server.Executable, string(args), string(env), server.RuntimeType, server.AutoStart, server.RestartPolicy, server.StopMethod, server.StopCommand, server.StopTimeoutSeconds, server.AutoRestartEnabled, server.AutoRestartMaxAttempts, server.AutoRestartWindowSeconds, server.AutoRestartDelaySeconds, stamp(server.UpdatedAt), id)
	if err != nil {
		return Record{}, err
	}
	return store.Get(ctx, id)
}
func (store *Store) Delete(ctx context.Context, id string) error {
	result, err := store.db.ExecContext(ctx, "DELETE FROM servers WHERE id=?", id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (store *Store) SaveRuntime(ctx context.Context, id string, state RuntimeState) error {
	now := time.Now().UTC()
	_, err := store.db.ExecContext(ctx, `UPDATE server_runtime_state SET pid=?,process_start_key=?,process_started_at=?,last_start_at=?,last_stop_at=?,last_exit_at=?,exit_code=?,last_crash_at=?,crash_count=?,restart_count=?,last_error=?,current_state=?,updated_at=? WHERE server_id=?`, nullableInt(state.PID), nullableString(state.processStartKey), nullableTime(state.ProcessStartAt), nullableTime(state.LastStartAt), nullableTime(state.LastStopAt), nullableTime(state.LastExitAt), nullableIntPtr(state.ExitCode), nullableTime(state.LastCrashAt), state.CrashCount, state.RestartCount, state.LastError, state.CurrentState, stamp(now), id)
	return err
}

const selectSQL = `SELECT s.id,s.creation_mode,s.name,s.description,s.working_directory,s.executable,s.arguments_json,s.environment_json,s.runtime_type,s.auto_start,s.restart_policy,s.stop_method,s.stop_command,s.stop_timeout_seconds,s.auto_restart_enabled,s.auto_restart_max_attempts,s.auto_restart_window_seconds,s.auto_restart_delay_seconds,s.created_at,s.updated_at,r.pid,r.process_start_key,r.process_started_at,r.last_start_at,r.last_stop_at,r.last_exit_at,r.exit_code,r.last_crash_at,r.crash_count,r.restart_count,r.last_error,r.current_state FROM servers s JOIN server_runtime_state r ON r.server_id=s.id`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Record, error) {
	var r Record
	var args, env string
	var auto, autoRestart int
	var pid sql.NullInt64
	var key sql.NullString
	var processStart, lastStart, lastStop, lastExit, lastCrash sql.NullString
	var exit sql.NullInt64
	var created, updated string
	err := row.Scan(&r.Server.ID, &r.Server.CreationMode, &r.Server.Name, &r.Server.Description, &r.Server.WorkingDirectory, &r.Server.Executable, &args, &env, &r.Server.RuntimeType, &auto, &r.Server.RestartPolicy, &r.Server.StopMethod, &r.Server.StopCommand, &r.Server.StopTimeoutSeconds, &autoRestart, &r.Server.AutoRestartMaxAttempts, &r.Server.AutoRestartWindowSeconds, &r.Server.AutoRestartDelaySeconds, &created, &updated, &pid, &key, &processStart, &lastStart, &lastStop, &lastExit, &exit, &lastCrash, &r.Runtime.CrashCount, &r.Runtime.RestartCount, &r.Runtime.LastError, &r.Runtime.CurrentState)
	if err != nil {
		return Record{}, err
	}
	r.Server.AutoStart = auto != 0
	r.Server.AutoRestartEnabled = autoRestart != 0
	_ = json.Unmarshal([]byte(args), &r.Server.Arguments)
	_ = json.Unmarshal([]byte(env), &r.Server.EnvironmentVariables)
	r.Server.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	r.Server.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if pid.Valid {
		r.Runtime.PID = int(pid.Int64)
	}
	r.Runtime.processStartKey = key.String
	r.Runtime.ProcessStartAt = parseTime(processStart)
	r.Runtime.LastStartAt = parseTime(lastStart)
	r.Runtime.LastStopAt = parseTime(lastStop)
	r.Runtime.LastExitAt = parseTime(lastExit)
	r.Runtime.LastCrashAt = parseTime(lastCrash)
	if exit.Valid {
		code := int(exit.Int64)
		r.Runtime.ExitCode = &code
	}
	return r, nil
}
func parseTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	v, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &v
}
func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return stamp(*value)
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
func nullableIntPtr(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[0:4]) + "-" + hex.EncodeToString(raw[4:6]) + "-4" + hex.EncodeToString(raw[6:8])[1:] + "-a" + hex.EncodeToString(raw[8:10])[1:] + "-" + hex.EncodeToString(raw[10:]), nil
}

type Service struct {
	store        *Store
	runtime      runtime.Runtime
	console      *console.Manager
	monitoring   *monitoring.Service
	ports        *ports.Service
	locks        sync.Map
	instances    sync.Map
	restarts     sync.Map
	autoRestarts sync.Map
	autoAttempts sync.Map
	autoMu       sync.Mutex
}

// processInstance binds one native process identity to the console session
// created for it. Its finalizer is the sole owner of exit cleanup.
type processInstance struct {
	serverID   string
	instanceID string
	identity   runtime.Identity
	session    *console.Session
	finalize   sync.Once
	done       chan struct{}
}
type pendingAutoRestart struct {
	generation string
	cancel     context.CancelFunc
}

// NewService accepts an optional ConsoleManager to preserve existing callers
// while allowing the application to own and inject the shared manager.
func NewService(store *Store, r runtime.Runtime, managers ...*console.Manager) *Service {
	manager := console.NewManager()
	if len(managers) > 0 && managers[0] != nil {
		manager = managers[0]
	}
	return NewServiceWithMonitoring(store, r, manager, monitoring.Options{})
}
func NewServiceWithMonitoring(store *Store, r runtime.Runtime, manager *console.Manager, options monitoring.Options) *Service {
	if manager == nil {
		manager = console.NewManager()
	}
	return &Service{store: store, runtime: r, console: manager, monitoring: monitoring.New(r, options), ports: ports.New(store.db)}
}
func (s *Service) Console() *console.Manager { return s.console }
func (s *Service) MonitoringSnapshot(ctx context.Context, id string) (monitoring.Snapshot, error) {
	record, err := s.refresh(ctx, id)
	if err != nil {
		return monitoring.Snapshot{}, err
	}
	identity := runtime.Identity{PID: record.Runtime.PID, StartKey: record.Runtime.processStartKey}
	detached := false
	if _, active := s.instances.Load(id); !active && record.Runtime.CurrentState == StateRunning {
		detached = true
		s.monitoring.ObserveRunning(id, identity, true)
	}
	pending, attempts, limited := s.autoRestartStatus(id, record.Server)
	return s.monitoring.Current(monitoring.Input{ServerID: id, State: record.Runtime.CurrentState, PID: record.Runtime.PID, Identity: identity, StartedAt: record.Runtime.ProcessStartAt, LastExitAt: record.Runtime.LastExitAt, LastExitCode: record.Runtime.ExitCode, CrashCount: record.Runtime.CrashCount, RestartCount: record.Runtime.RestartCount, LastError: record.Runtime.LastError, Detached: detached, AutoRestartEnabled: record.Server.AutoRestartEnabled, PendingAutoRestart: pending, AutoRestartAttempts: attempts, RestartLimitReached: limited}), nil
}
func (s *Service) MonitoringHistory(ctx context.Context, id string) ([]monitoring.Sample, error) {
	_, err := s.refresh(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.monitoring.History(id), nil
}
func (s *Service) Create(ctx context.Context, server Server) (Record, error) {
	return s.store.Create(ctx, server)
}
func (s *Service) List(ctx context.Context) ([]Record, error)         { return s.refreshAll(ctx) }
func (s *Service) Get(ctx context.Context, id string) (Record, error) { return s.refresh(ctx, id) }

// Rediscover refreshes persisted processes after GameNode starts. A verified
// surviving process is running but deliberately detached from console I/O.
func (s *Service) Rediscover(ctx context.Context) error {
	_, err := s.refreshAll(ctx)
	return err
}
func (s *Service) Update(ctx context.Context, id string, server Server) (Record, error) {
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	record, err := s.refresh(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.Runtime.CurrentState == StateRunning || record.Runtime.CurrentState == StateStarting || record.Runtime.CurrentState == StateStopping {
		return Record{}, errors.New("stop the server before editing")
	}
	return s.store.Update(ctx, id, server)
}
func (s *Service) Delete(ctx context.Context, id string) error {
	s.cancelAutoRestart(id)
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	record, err := s.refresh(ctx, id)
	if err != nil {
		return err
	}
	if record.Runtime.CurrentState == StateRunning || record.Runtime.CurrentState == StateStarting || record.Runtime.CurrentState == StateStopping {
		return errors.New("stop the server before deleting")
	}
	return s.store.Delete(ctx, id)
}
func (s *Service) Start(ctx context.Context, id string) (Record, error) {
	s.cancelAutoRestart(id)
	return s.start(ctx, id, false)
}

func (s *Service) start(ctx context.Context, id string, restart bool) (Record, error) {
	if !restart {
		if _, active := s.restarts.Load(id); active {
			return Record{}, errors.New("server restart is in progress")
		}
	}
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	record, err := s.refresh(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.Runtime.CurrentState == StateRunning || record.Runtime.CurrentState == StateStarting || record.Runtime.CurrentState == StateStopping {
		return Record{}, errors.New("server is already running")
	}
	// Preflight before state mutation, console-session creation, or Runtime.Start.
	if err = s.ports.Check(ctx, id); err != nil {
		preflight := fmt.Errorf("port preflight: %w", err)
		// Keep the finalized state (stopped for a manual restart, crashed for an
		// auto-restart) and make the failed normal start observable. This is not
		// a process crash and must not schedule another auto-restart.
		record.Runtime.LastError = preflight.Error()
		_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
		return Record{}, preflight
	}
	now := time.Now().UTC()
	record.Runtime.CurrentState = StateStarting
	record.Runtime.LastError = ""
	if err = s.store.SaveRuntime(ctx, id, record.Runtime); err != nil {
		return Record{}, err
	}
	instanceID, err := newID()
	if err != nil {
		return Record{}, err
	}
	session := s.console.CreateSession(id, instanceID)
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			session.Close(StateStopped)
			s.console.RemoveSession(id, session.ID)
		})
	}
	identity, exits, err := s.runtime.Start(ctx, runtime.StartOptions{
		Executable:       record.Server.ResolvedExecutable(),
		Arguments:        record.Server.Arguments,
		WorkingDirectory: record.Server.WorkingDirectory,
		Environment:      record.Server.EnvironmentVariables,
		IO: runtime.StartIO{
			Stdout: session.Output("stdout"),
			Stderr: session.Output("stderr"),
			Stdin:  session.AttachInput,
		},
	})
	if err != nil {
		cleanup()
		record.Runtime.CurrentState = StateStopped
		record.Runtime.LastError = "start failed"
		_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
		return Record{}, err
	}
	record.Runtime.PID = identity.PID
	record.Runtime.processStartKey = identity.StartKey
	record.Runtime.ProcessStartAt = &now
	record.Runtime.LastStartAt = &now
	if restart {
		record.Runtime.RestartCount++
	}
	record.Runtime.ExitCode = nil
	record.Runtime.CurrentState = StateRunning
	s.monitoring.ObserveRunning(id, identity, false)
	if err = s.store.SaveRuntime(ctx, id, record.Runtime); err != nil {
		return Record{}, err
	}
	instance := &processInstance{serverID: id, instanceID: instanceID, identity: identity, session: session, done: make(chan struct{})}
	s.instances.Store(id, instance)
	go s.captureExit(instance, exits)
	return s.store.Get(ctx, id)
}
func (s *Service) Stop(ctx context.Context, id string) (Record, error) {
	return s.signal(ctx, id, false)
}
func (s *Service) Kill(ctx context.Context, id string) (Record, error) {
	return s.signal(ctx, id, true)
}
func (s *Service) Restart(ctx context.Context, id string) (Record, error) {
	s.cancelAutoRestart(id)
	if _, loaded := s.restarts.LoadOrStore(id, struct{}{}); loaded {
		return Record{}, errors.New("server restart is already in progress")
	}
	defer s.restarts.Delete(id)
	if _, err := s.signalWithRestart(ctx, id, false, true); err != nil {
		return Record{}, err
	}
	return s.start(ctx, id, true)
}
func (s *Service) signal(ctx context.Context, id string, kill bool) (Record, error) {
	s.cancelAutoRestart(id)
	return s.signalWithRestart(ctx, id, kill, false)
}

func (s *Service) signalWithRestart(ctx context.Context, id string, kill, restart bool) (Record, error) {
	if !restart {
		if _, active := s.restarts.Load(id); active {
			return Record{}, errors.New("server restart is in progress")
		}
	}
	lock := s.lock(id)
	lock.Lock()
	// The lock is released before waiting for the exit goroutine.
	record, err := s.refresh(ctx, id)
	if err != nil {
		lock.Unlock()
		return Record{}, err
	}
	if record.Runtime.CurrentState != StateRunning {
		lock.Unlock()
		return Record{}, errors.New("server is not running")
	}
	record.Runtime.CurrentState = StateStopping
	_ = s.store.SaveRuntime(ctx, id, record.Runtime)
	identity := runtime.Identity{PID: record.Runtime.PID, StartKey: record.Runtime.processStartKey}
	if kill {
		err = s.runtime.Kill(ctx, identity)
	} else {
		err = s.runtime.Stop(ctx, identity, time.Duration(record.Server.StopTimeoutSeconds)*time.Second)
	}
	if err != nil {
		record.Runtime.CurrentState = StateUnknown
		record.Runtime.LastError = "lifecycle operation failed"
		_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
		lock.Unlock()
		return Record{}, err
	}
	instance, _ := s.instances.Load(id)
	lock.Unlock()
	if process, ok := instance.(*processInstance); ok && process.identity == identity {
		<-process.done
	}
	return s.store.Get(ctx, id)
}
func (s *Service) captureExit(instance *processInstance, exits <-chan runtime.ExitResult) {
	exit, ok := <-exits
	if ok {
		s.finalizeInstance(instance, exit)
	}
}

// finalizeInstance is the sole exit cleanup path. It is idempotent and only
// mutates persisted state if the captured process identity still matches.
func (s *Service) finalizeInstance(instance *processInstance, exit runtime.ExitResult) {
	instance.finalize.Do(func() {
		defer close(instance.done)
		lock := s.lock(instance.serverID)
		lock.Lock()
		defer lock.Unlock()

		sessionState := StateStopped
		if exit.ExitCode != 0 {
			sessionState = StateCrashed
		}
		instance.session.Close(sessionState)
		s.console.ClearCurrentSession(instance.serverID, instance.session.ID)

		record, err := s.store.Get(context.Background(), instance.serverID)
		if err == nil && record.Runtime.PID == instance.identity.PID && record.Runtime.processStartKey == instance.identity.StartKey {
			now := time.Now().UTC()
			stopping := record.Runtime.CurrentState == StateStopping
			record.Runtime.PID = 0
			record.Runtime.processStartKey = ""
			record.Runtime.ProcessStartAt = nil
			record.Runtime.ExitCode = &exit.ExitCode
			record.Runtime.LastExitAt = &now
			record.Runtime.LastStopAt = &now
			if stopping || exit.ExitCode == 0 {
				record.Runtime.CurrentState = StateStopped
			} else {
				record.Runtime.CurrentState = StateCrashed
				record.Runtime.LastCrashAt = &now
				record.Runtime.CrashCount++
			}
			if exit.Err != nil && !stopping {
				record.Runtime.LastError = "process exited"
			}
			_ = s.store.SaveRuntime(context.Background(), instance.serverID, record.Runtime)
			s.monitoring.ObserveExit(instance.serverID, instance.identity)
			if record.Runtime.CurrentState == StateCrashed {
				s.scheduleAutoRestart(instance.serverID, instance.instanceID, record.Server)
			}
		}
		s.instances.CompareAndDelete(instance.serverID, instance)
	})
}
func (s *Service) cancelAutoRestart(id string) {
	if value, ok := s.autoRestarts.LoadAndDelete(id); ok {
		value.(*pendingAutoRestart).cancel()
	}
}
func (s *Service) scheduleAutoRestart(id, generation string, server Server) {
	if !server.AutoRestartEnabled {
		return
	}
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	now := time.Now().UTC()
	var attempts []time.Time
	if value, ok := s.autoAttempts.Load(id); ok {
		attempts = value.([]time.Time)
	}
	cutoff := now.Add(-time.Duration(server.AutoRestartWindowSeconds) * time.Second)
	kept := attempts[:0]
	for _, attempt := range attempts {
		if attempt.After(cutoff) {
			kept = append(kept, attempt)
		}
	}
	if len(kept) >= server.AutoRestartMaxAttempts {
		record, err := s.store.Get(context.Background(), id)
		if err == nil {
			record.Runtime.LastError = "auto restart limit reached"
			_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
		}
		s.autoAttempts.Store(id, kept)
		return
	}
	kept = append(kept, now)
	s.autoAttempts.Store(id, kept)
	ctx, cancel := context.WithCancel(context.Background())
	pending := &pendingAutoRestart{generation: generation, cancel: cancel}
	if old, loaded := s.autoRestarts.Swap(id, pending); loaded {
		old.(*pendingAutoRestart).cancel()
	}
	go func() {
		timer := time.NewTimer(time.Duration(server.AutoRestartDelaySeconds) * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		current, ok := s.autoRestarts.LoadAndDelete(id)
		if !ok || current != pending {
			return
		}
		// The finalizer has already persisted crashed state; this starts a fresh
		// instance/session through the normal orchestration path.
		_, _ = s.start(context.Background(), id, true)
	}()
}
func (s *Service) autoRestartStatus(id string, server Server) (bool, int, bool) {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	_, pending := s.autoRestarts.Load(id)
	var attempts []time.Time
	if value, ok := s.autoAttempts.Load(id); ok {
		attempts = value.([]time.Time)
	}
	cutoff := time.Now().Add(-time.Duration(server.AutoRestartWindowSeconds) * time.Second)
	count := 0
	for _, attempt := range attempts {
		if attempt.After(cutoff) {
			count++
		}
	}
	return pending, count, server.AutoRestartEnabled && count >= server.AutoRestartMaxAttempts
}
func (s *Service) refreshAll(ctx context.Context) ([]Record, error) {
	records, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range records {
		records[i] = s.refreshRecord(ctx, records[i])
	}
	return records, nil
}
func (s *Service) refresh(ctx context.Context, id string) (Record, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	return s.refreshRecord(ctx, record), nil
}
func (s *Service) refreshRecord(ctx context.Context, record Record) Record {
	if _, active := s.instances.Load(record.Server.ID); active {
		// A launched process is finalized exclusively by its Wait/exit path.
		return record
	}
	if record.Runtime.PID == 0 || record.Runtime.processStartKey == "" {
		return record
	}
	status, err := s.runtime.Status(ctx, runtime.Identity{PID: record.Runtime.PID, StartKey: record.Runtime.processStartKey})
	if err != nil {
		return record
	}
	if status.Running {
		record.Runtime.CurrentState = StateRunning
		_ = s.store.SaveRuntime(context.Background(), record.Server.ID, record.Runtime)
		s.console.MarkDetached(record.Server.ID)
		return record
	}
	if !status.Known {
		record.Runtime.CurrentState = StateUnknown
		record.Runtime.LastError = "process identity could not be verified"
		_ = s.store.SaveRuntime(context.Background(), record.Server.ID, record.Runtime)
		return record
	}
	now := time.Now().UTC()
	record.Runtime.CurrentState = StateStopped
	record.Runtime.LastStopAt = &now
	record.Runtime.PID = 0
	record.Runtime.processStartKey = ""
	record.Runtime.ProcessStartAt = nil
	_ = s.store.SaveRuntime(context.Background(), record.Server.ID, record.Runtime)
	return record
}
func (s *Service) lock(id string) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}
