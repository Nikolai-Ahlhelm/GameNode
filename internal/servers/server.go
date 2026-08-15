package servers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	CreationNew      = "new"
	CreationAdopt    = "adopt"
	CreationCustom   = "custom"
	CreationTemplate = "template"
	StateStopped     = "stopped"
	StateRunning     = "running"
	StateStarting    = "starting"
	StateStopping    = "stopping"
	StateCrashed     = "crashed"
	StateUnknown     = "unknown"

	// StopMethodConsoleInterrupt is a compiled, Windows-only stop type. A
	// process started with this stop method receives a targeted Windows
	// console control event instead of being terminated outright; there is no
	// free-form stop string or template-defined signal number behind it. See
	// internal/runtime.Interrupt and docs/runtime.md.
	StopMethodConsoleInterrupt = "console_interrupt"
)

var (
	environmentKey              = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	serverIDPattern             = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-a[0-9a-f]{3}-[0-9a-f]{12}$`)
	ErrProvisionedPortConflict  = errors.New("provisioned port conflicts with another server")
	ErrInvalidProvisionedPort   = errors.New("invalid provisioned port")
	ErrProvisionedPortsConflict = errors.New("provisioned ports conflict")
	ErrProvisionedConfigAdapter = errors.New("provisioned configuration adapter could not be stored")
)

type Server struct {
	ID                            string            `json:"id"`
	CreationMode                  string            `json:"creation_mode"`
	Name                          string            `json:"name"`
	Description                   string            `json:"description"`
	WorkingDirectory              string            `json:"working_directory"`
	Executable                    string            `json:"executable"`
	Arguments                     []string          `json:"arguments"`
	EnvironmentVariables          map[string]string `json:"environment_variables"`
	SensitiveEnvironmentVariables []string          `json:"sensitive_environment_variables,omitempty"`
	RuntimeType                   string            `json:"runtime_type"`
	AutoStart                     bool              `json:"auto_start"`
	RestartPolicy                 string            `json:"restart_policy"`
	StopMethod                    string            `json:"stop_method"`
	StopCommand                   string            `json:"stop_command"`
	StopTimeoutSeconds            int               `json:"stop_timeout_seconds"`
	AutoRestartEnabled            bool              `json:"auto_restart_enabled"`
	AutoRestartMaxAttempts        int               `json:"auto_restart_max_attempts"`
	AutoRestartWindowSeconds      int               `json:"auto_restart_window_seconds"`
	AutoRestartDelaySeconds       int               `json:"auto_restart_delay_seconds"`
	CreatedAt                     time.Time         `json:"created_at"`
	UpdatedAt                     time.Time         `json:"updated_at"`
}

type RuntimeState struct {
	PID            int        `json:"pid,omitempty"`
	ProcessStartAt *time.Time `json:"process_started_at,omitempty"`
	LastStartAt    *time.Time `json:"last_start_at,omitempty"`
	LastStopAt     *time.Time `json:"last_stop_at,omitempty"`
	LastExitAt     *time.Time `json:"last_exit_at,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	LastCrashAt    *time.Time `json:"last_crash_at,omitempty"`
	CrashCount     int        `json:"crash_count"`
	RestartCount   int        `json:"restart_count"`
	LastError      string     `json:"last_error,omitempty"`
	CurrentState   string     `json:"current_state"`
	// ConsoleDetached is runtime-only state. It is true only when GameNode has
	// identity-verified a process that survived a GameNode restart; its original
	// stdout/stderr/stdin pipes cannot be recovered.
	ConsoleDetached bool `json:"console_detached,omitempty"`
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
	if s.CreationMode != CreationNew && s.CreationMode != CreationAdopt && s.CreationMode != CreationCustom && s.CreationMode != CreationTemplate {
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
	if s.StopMethod != "terminate" && s.StopMethod != "stdin_command" && s.StopMethod != StopMethodConsoleInterrupt {
		return errors.New("stop method must be terminate, stdin_command, or console_interrupt")
	}
	if s.StopMethod == "stdin_command" {
		if strings.TrimSpace(s.StopCommand) == "" || len(s.StopCommand) > 256 || strings.ContainsAny(s.StopCommand, "\r\n\x00") {
			return errors.New("stdin stop command must be one non-empty line")
		}
	} else if s.StopCommand != "" {
		return errors.New("stop command requires stdin_command stop method")
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

type ProvisionedVariable struct {
	Key       string
	Sensitive bool
	Source    string
	Version   string
}

type ProvisionedConfigAdapter struct {
	ID              string
	SchemaVersion   int
	Version         string
	TemplateID      string
	TemplateVersion string
	DefinitionJSON  []byte
	Values          []ProvisionedConfigValue
}

// ProvisionedConfigValue is an initial managed configuration value. Values are
// persisted in the same transaction as the server so a failed registration
// cannot leave a partially configured server behind.
type ProvisionedConfigValue struct {
	Key       string
	Value     string
	Sensitive bool
}

// CreateProvisioned atomically publishes a fully installed native server and
// its template-variable sensitivity metadata. Filesystem installation has
// already completed and is deliberately outside this database transaction.
func (store *Store) CreateProvisioned(ctx context.Context, server Server, templateID string, variables []ProvisionedVariable, provisionedPorts []ports.Port, configAdapters []ProvisionedConfigAdapter) (Record, error) {
	if err := server.Validate(); err != nil {
		return Record{}, err
	}
	if server.ID == "" {
		id, err := newID()
		if err != nil {
			return Record{}, err
		}
		server.ID = id
	} else if !serverIDPattern.MatchString(server.ID) {
		return Record{}, errors.New("invalid provisioned server id")
	}
	now := time.Now().UTC()
	server.CreatedAt = now
	server.UpdatedAt = now
	args, _ := json.Marshal(server.Arguments)
	env, _ := json.Marshal(server.EnvironmentVariables)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO servers(id,creation_mode,name,description,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,auto_restart_enabled,auto_restart_max_attempts,auto_restart_window_seconds,auto_restart_delay_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, server.ID, server.CreationMode, server.Name, server.Description, server.WorkingDirectory, server.Executable, string(args), string(env), server.RuntimeType, server.AutoStart, server.RestartPolicy, server.StopMethod, server.StopCommand, server.StopTimeoutSeconds, server.AutoRestartEnabled, server.AutoRestartMaxAttempts, server.AutoRestartWindowSeconds, server.AutoRestartDelaySeconds, stamp(now), stamp(now))
	if err != nil {
		return Record{}, fmt.Errorf("create provisioned server: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO server_runtime_state(server_id,current_state,updated_at) VALUES(?,?,?)`, server.ID, StateStopped, stamp(now)); err != nil {
		return Record{}, err
	}
	for _, variable := range variables {
		if !environmentKey.MatchString(variable.Key) {
			return Record{}, errors.New("invalid provisioned variable key")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO server_template_variables(server_id,template_id,variable_key,sensitive,template_source,template_version) VALUES(?,?,?,?,?,?)`, server.ID, templateID, variable.Key, variable.Sensitive, variable.Source, variable.Version); err != nil {
			return Record{}, err
		}
	}
	existingRows, err := tx.QueryContext(ctx, `SELECT name,protocol,bind_address,port FROM server_ports`)
	if err != nil {
		return Record{}, err
	}
	var existingPorts []ports.Port
	for existingRows.Next() {
		var existing ports.Port
		if err = existingRows.Scan(&existing.Name, &existing.Protocol, &existing.BindAddress, &existing.Port); err != nil {
			existingRows.Close()
			return Record{}, err
		}
		existingPorts = append(existingPorts, existing)
	}
	if err = existingRows.Close(); err != nil {
		return Record{}, err
	}
	for index := range provisionedPorts {
		candidate := provisionedPorts[index]
		if err = ports.Validate(&candidate); err != nil {
			return Record{}, ErrInvalidProvisionedPort
		}
		for _, existing := range existingPorts {
			if ports.Conflict(candidate, existing) {
				return Record{}, ErrProvisionedPortConflict
			}
		}
		for prior := 0; prior < index; prior++ {
			if ports.Conflict(candidate, provisionedPorts[prior]) {
				return Record{}, ErrProvisionedPortsConflict
			}
		}
		provisionedPorts[index] = candidate
	}
	for _, provisionedPort := range provisionedPorts {
		portID, idErr := newID()
		if idErr != nil {
			return Record{}, idErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO server_ports(id,server_id,name,protocol,bind_address,port,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, portID, server.ID, provisionedPort.Name, provisionedPort.Protocol, provisionedPort.BindAddress, provisionedPort.Port, stamp(now), stamp(now)); err != nil {
			return Record{}, err
		}
	}
	for _, adapter := range configAdapters {
		if !environmentKey.MatchString(strings.ReplaceAll(adapter.ID, "-", "_")) || (adapter.SchemaVersion != 1 && adapter.SchemaVersion != 2) || adapter.Version == "" || adapter.TemplateID != templateID || len(adapter.DefinitionJSON) == 0 || len(adapter.DefinitionJSON) > 128<<10 || !json.Valid(adapter.DefinitionJSON) {
			return Record{}, ErrProvisionedConfigAdapter
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO server_config_adapters(server_id,adapter_id,adapter_schema_version,adapter_version,template_id,template_version,definition_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, server.ID, adapter.ID, adapter.SchemaVersion, adapter.Version, adapter.TemplateID, adapter.TemplateVersion, string(adapter.DefinitionJSON), stamp(now), stamp(now)); err != nil {
			return Record{}, fmt.Errorf("%w: %v", ErrProvisionedConfigAdapter, err)
		}
		seenValues := map[string]bool{}
		for _, value := range adapter.Values {
			if !environmentKey.MatchString(value.Key) || seenValues[value.Key] || len(value.Value) > 16<<10 || strings.ContainsRune(value.Value, 0) {
				return Record{}, ErrProvisionedConfigAdapter
			}
			seenValues[value.Key] = true
			if _, err = tx.ExecContext(ctx, `INSERT INTO server_config_values(server_id,adapter_id,field_key,value,sensitive,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, server.ID, adapter.ID, value.Key, value.Value, value.Sensitive, stamp(now), stamp(now)); err != nil {
				return Record{}, fmt.Errorf("%w: %v", ErrProvisionedConfigAdapter, err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return Record{}, err
	}
	return Record{Server: server, Runtime: RuntimeState{CurrentState: StateStopped}}, nil
}

func (store *Store) SensitiveEnvironmentKeys(ctx context.Context, id string) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT variable_key FROM server_template_variables WHERE server_id=? AND sensitive=1 ORDER BY variable_key`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
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
	sensitive, err := store.SensitiveEnvironmentKeys(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if server.EnvironmentVariables == nil {
		server.EnvironmentVariables = map[string]string{}
	}
	for _, key := range sensitive {
		if value, ok := server.EnvironmentVariables[key]; !ok || value == "********" {
			server.EnvironmentVariables[key] = existing.Server.EnvironmentVariables[key]
		}
	}
	if len(sensitive) > 0 {
		for index, argument := range server.Arguments {
			if strings.Contains(argument, "********") && index < len(existing.Server.Arguments) {
				server.Arguments[index] = existing.Server.Arguments[index]
			}
		}
	}
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
	deletions    sync.Map
	autoRestarts sync.Map
	autoAttempts sync.Map
	autoMu       sync.Mutex
	log          *slog.Logger
	launch       LaunchResolver
}

// LaunchResolver expands the persisted base launch with reviewed managed
// configuration immediately before the process starts. It is optional; a nil
// resolver leaves the persisted executable, arguments, and environment
// untouched. Implementations must return a complete argv/environment pair and
// must never be given the opportunity to persist secret values.
type LaunchResolver interface {
	ResolveLaunch(ctx context.Context, serverID string, arguments []string, environment map[string]string) ([]string, map[string]string, error)
}

// SetLaunchResolver installs the managed configuration resolver. The
// composition root owns this wiring so internal/servers keeps no dependency on
// the configuration package.
func (s *Service) SetLaunchResolver(resolver LaunchResolver) { s.launch = resolver }

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
	return &Service{store: store, runtime: r, console: manager, monitoring: monitoring.New(r, options), ports: ports.New(store.db), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// SetLogger connects the application logger before the service begins serving requests.
func (s *Service) SetLogger(log *slog.Logger) {
	if log != nil {
		s.log = log
		s.monitoring.SetLogger(log)
	}
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
	record, err := s.store.Create(ctx, server)
	if err != nil {
		s.log.Error("server registration failed", "module", "Server.Create", "error", err)
		return Record{}, err
	}
	s.log.Info("server registered", "module", "Server.Create", "server_id", record.Server.ID, "creation_mode", record.Server.CreationMode)
	return record, nil
}
func (s *Service) CreateProvisioned(ctx context.Context, server Server, templateID string, variables []ProvisionedVariable, provisionedPorts []ports.Port, configAdapters []ProvisionedConfigAdapter) (Record, error) {
	record, err := s.store.CreateProvisioned(ctx, server, templateID, variables, provisionedPorts, configAdapters)
	if err != nil {
		s.log.Error("provisioned server registration failed", "module", "Server.Create", "template_id", templateID, "error", err)
		return Record{}, err
	}
	s.log.Info("provisioned server registered", "module", "Server.Create", "server_id", record.Server.ID, "template_id", templateID, "ports", len(provisionedPorts), "config_adapters", len(configAdapters))
	return record, nil
}
func (s *Service) SensitiveEnvironmentKeys(ctx context.Context, id string) ([]string, error) {
	return s.store.SensitiveEnvironmentKeys(ctx, id)
}
func (s *Service) List(ctx context.Context) ([]Record, error)         { return s.refreshAll(ctx) }
func (s *Service) Get(ctx context.Context, id string) (Record, error) { return s.refresh(ctx, id) }

// Rediscover refreshes persisted processes after GameNode starts. A verified
// surviving process is running but deliberately detached from console I/O.
func (s *Service) Rediscover(ctx context.Context) error {
	s.log.Info("server process rediscovery started", "module", "Server.Rediscovery")
	records, err := s.refreshAll(ctx)
	if err != nil {
		s.log.Error("server process rediscovery failed", "module", "Server.Rediscovery", "error", err)
		return err
	}
	running := 0
	for _, record := range records {
		if record.Runtime.CurrentState == StateRunning {
			running++
		}
	}
	s.log.Info("server process rediscovery completed", "module", "Server.Rediscovery", "servers", len(records), "running", running)
	return err
}
func (s *Service) Update(ctx context.Context, id string, server Server) (Record, error) {
	s.log.Info("server update started", "module", "Server.Update", "server_id", id)
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	record, err := s.refresh(ctx, id)
	if err != nil {
		s.log.Error("server update failed while loading state", "module", "Server.Update", "server_id", id, "error", err)
		return Record{}, err
	}
	if record.Runtime.CurrentState == StateRunning || record.Runtime.CurrentState == StateStarting || record.Runtime.CurrentState == StateStopping {
		err = errors.New("stop the server before editing")
		s.log.Warn("server update rejected by lifecycle state", "module", "Server.Update", "server_id", id, "state", record.Runtime.CurrentState, "error", err)
		return Record{}, err
	}
	updated, err := s.store.Update(ctx, id, server)
	if err != nil {
		s.log.Error("server update failed", "module", "Server.Update", "server_id", id, "error", err)
		return Record{}, err
	}
	s.log.Info("server update completed", "module", "Server.Update", "server_id", id)
	return updated, nil
}
func (s *Service) Delete(ctx context.Context, id string) error {
	s.log.Info("server deletion started", "module", "Server.Delete", "server_id", id)
	if _, loaded := s.deletions.LoadOrStore(id, struct{}{}); loaded {
		return errors.New("server deletion is already in progress")
	}
	defer s.deletions.Delete(id)
	s.cancelAutoRestart(id)

	// Deletion owns the lifecycle until the row is gone. If a native process is
	// still associated with the server, terminate that exact PID/start identity
	// and wait for its normal exactly-once finalizer before deleting the row.
	// The working directory is intentionally not removed: adopted and custom
	// servers can point at user-owned or shared data.
	lock := s.lock(id)
	lock.Lock()
	record, err := s.refresh(ctx, id)
	if err != nil {
		lock.Unlock()
		s.log.Error("server deletion failed while loading state", "module", "Server.Delete", "server_id", id, "error", err)
		return err
	}
	identity := runtime.Identity{PID: record.Runtime.PID, StartKey: record.Runtime.processStartKey}
	instance, attached := s.instances.Load(id)
	process, attached := instance.(*processInstance)
	attached = attached && process.identity == identity
	if identity.PID != 0 && identity.StartKey != "" {
		record.Runtime.CurrentState = StateStopping
		if err = s.store.SaveRuntime(ctx, id, record.Runtime); err != nil {
			lock.Unlock()
			return err
		}
		err = s.runtime.Kill(ctx, identity)
		if err != nil && !errors.Is(err, runtime.ErrNotRunning) {
			record.Runtime.CurrentState = StateUnknown
			record.Runtime.LastError = "deletion could not terminate process"
			_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
			lock.Unlock()
			s.log.Error("server deletion could not terminate native process", "module", "Server.Delete", "server_id", id, "pid", identity.PID, "error", err)
			return fmt.Errorf("terminate server before deletion: %w", err)
		}
		lock.Unlock()

		if attached {
			if errors.Is(err, runtime.ErrNotRunning) {
				s.finalizeInstance(process, runtime.ExitResult{ExitCode: 0})
			}
			select {
			case <-process.done:
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			// Rediscovered processes have no in-memory wait/finalizer. Kill has
			// synchronously accepted termination for this verified identity, so
			// clear its persisted runtime identity before removing the row.
			lock.Lock()
			record.Runtime.PID = 0
			record.Runtime.processStartKey = ""
			record.Runtime.ProcessStartAt = nil
			record.Runtime.CurrentState = StateStopped
			record.Runtime.ConsoleDetached = false
			_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
			s.monitoring.ObserveExit(id, identity)
			lock.Unlock()
		}

		lock.Lock()
		record, err = s.refresh(ctx, id)
		if err != nil {
			lock.Unlock()
			return err
		}
	}
	if err = s.store.Delete(ctx, id); err != nil {
		lock.Unlock()
		s.log.Error("server deletion failed", "module", "Server.Delete", "server_id", id, "error", err)
		return err
	}
	lock.Unlock()
	s.log.Info("server deleted", "module", "Server.Delete", "server_id", id)
	return nil
}
func (s *Service) Start(ctx context.Context, id string) (Record, error) {
	s.log.Info("server start requested", "module", "Server.Start", "server_id", id)
	if _, deleting := s.deletions.Load(id); deleting {
		return Record{}, errors.New("server deletion is in progress")
	}
	s.cancelAutoRestart(id)
	// A Windows child can terminate while Cmd.Wait is still waiting for a
	// descendant that inherited its console pipes. Do not let that delayed
	// callback permanently block a subsequent manual start behind a stale
	// in-memory instance/session.
	s.reconcileExitedActive(ctx, id)
	return s.start(ctx, id, false)
}

func (s *Service) reconcileExitedActive(ctx context.Context, id string) {
	value, ok := s.instances.Load(id)
	if !ok {
		return
	}
	instance, ok := value.(*processInstance)
	if !ok {
		return
	}
	status, err := s.runtime.Status(ctx, instance.identity)
	if err != nil || !status.Known || status.Running {
		return
	}
	// This is only a fallback when the OS has already identity-verified that
	// the process is gone. The normal Wait callback remains the authoritative
	// source of real exit codes.
	s.finalizeInstance(instance, runtime.ExitResult{ExitCode: 0})
}

func (s *Service) start(ctx context.Context, id string, restart bool) (Record, error) {
	operation := "start"
	if restart {
		operation = "restart start"
	}
	s.log.Info("server process start preparing", "module", "Server.Start", "server_id", id, "operation", operation)
	if _, deleting := s.deletions.Load(id); deleting {
		return Record{}, errors.New("server deletion is in progress")
	}
	if !restart {
		if _, active := s.restarts.Load(id); active {
			err := errors.New("server restart is in progress")
			s.log.Warn("server start rejected", "module", "Server.Start", "server_id", id, "error", err)
			return Record{}, err
		}
	}
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	if _, deleting := s.deletions.Load(id); deleting {
		return Record{}, errors.New("server deletion is in progress")
	}
	record, err := s.refresh(ctx, id)
	if err != nil {
		s.log.Error("server process start failed while loading state", "module", "Server.Start", "server_id", id, "error", err)
		return Record{}, err
	}
	if record.Runtime.CurrentState == StateRunning || record.Runtime.CurrentState == StateStarting || record.Runtime.CurrentState == StateStopping {
		err = errors.New("server is already running")
		s.log.Warn("server process start rejected by lifecycle state", "module", "Server.Start", "server_id", id, "state", record.Runtime.CurrentState, "error", err)
		return Record{}, err
	}
	if record.Runtime.CurrentState == StateUnknown && record.Runtime.PID != 0 && record.Runtime.processStartKey != "" {
		err = errors.New("server process status could not be verified")
		s.log.Warn("server process start rejected because existing identity is unverified", "module", "Server.Start", "server_id", id, "pid", record.Runtime.PID, "error", err)
		return Record{}, err
	}
	// Preflight before state mutation, console-session creation, or Runtime.Start.
	if err = s.ports.Check(ctx, id); err != nil {
		preflight := fmt.Errorf("port preflight: %w", err)
		// Keep the finalized state (stopped for a manual restart, crashed for an
		// auto-restart) and make the failed normal start observable. This is not
		// a process crash and must not schedule another auto-restart.
		record.Runtime.LastError = preflight.Error()
		_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
		s.log.Error("server port preflight failed", "module", "Server.Start", "server_id", id, "error", preflight)
		return Record{}, preflight
	}
	// Managed configuration is expanded after preflight but before any state
	// mutation, console session, or process start, so an incomplete or invalid
	// configuration fails like a preflight error rather than a crash. The
	// resolved values stay in memory and are never persisted.
	arguments, environment := record.Server.Arguments, record.Server.EnvironmentVariables
	if s.launch != nil {
		arguments, environment, err = s.launch.ResolveLaunch(ctx, id, arguments, environment)
		if err != nil {
			resolution := fmt.Errorf("managed configuration: %w", err)
			record.Runtime.LastError = resolution.Error()
			_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
			s.log.Error("managed game configuration could not be resolved", "module", "Server.Start", "server_id", id, "error", resolution)
			return Record{}, resolution
		}
	}
	now := time.Now().UTC()
	record.Runtime.CurrentState = StateStarting
	record.Runtime.LastError = ""
	if err = s.store.SaveRuntime(ctx, id, record.Runtime); err != nil {
		s.log.Error("server starting state could not be persisted", "module", "Server.Start", "server_id", id, "error", err)
		return Record{}, err
	}
	instanceID, err := newID()
	if err != nil {
		s.log.Error("server process instance ID creation failed", "module", "Server.Start", "server_id", id, "error", err)
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
		Arguments:        arguments,
		WorkingDirectory: record.Server.WorkingDirectory,
		Environment:      environment,
		IO: runtime.StartIO{
			Stdout: session.Output("stdout"),
			Stderr: session.Output("stderr"),
			Stdin:  session.AttachInput,
		},
		// Derived from the normalized stop method, never from template or
		// request data directly, so only a console_interrupt server changes
		// how its process group is created.
		ConsoleInterruptCapable: record.Server.StopMethod == StopMethodConsoleInterrupt,
	})
	if err != nil {
		cleanup()
		record.Runtime.CurrentState = StateStopped
		record.Runtime.LastError = "start failed"
		_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
		s.log.Error("native server process failed to start", "module", "Server.Start", "server_id", id, "error", err)
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
		s.log.Error("running server state could not be persisted", "module", "Server.Start", "server_id", id, "pid", identity.PID, "error", err)
		return Record{}, err
	}
	instance := &processInstance{serverID: id, instanceID: instanceID, identity: identity, session: session, done: make(chan struct{})}
	s.instances.Store(id, instance)
	go s.captureExit(instance, exits)
	s.log.Info("native server process started", "module", "Server.Start", "server_id", id, "pid", identity.PID, "restart", restart)
	result, err := s.store.Get(ctx, id)
	if err != nil {
		s.log.Error("started server state could not be loaded", "module", "Server.Start", "server_id", id, "pid", identity.PID, "error", err)
		return Record{}, err
	}
	return result, nil
}
func (s *Service) Stop(ctx context.Context, id string) (Record, error) {
	s.log.Info("server stop requested", "module", "Server.Stop", "server_id", id)
	return s.signal(ctx, id, false)
}
func (s *Service) Kill(ctx context.Context, id string) (Record, error) {
	s.log.Warn("server kill requested", "module", "Server.Kill", "server_id", id)
	return s.signal(ctx, id, true)
}
func (s *Service) Restart(ctx context.Context, id string) (Record, error) {
	s.log.Info("server restart requested", "module", "Server.Restart", "server_id", id)
	s.cancelAutoRestart(id)
	if _, loaded := s.restarts.LoadOrStore(id, struct{}{}); loaded {
		err := errors.New("server restart is already in progress")
		s.log.Warn("server restart rejected", "module", "Server.Restart", "server_id", id, "error", err)
		return Record{}, err
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
	module := "Server.Stop"
	if kill {
		module = "Server.Kill"
	}
	if !restart {
		if _, active := s.restarts.Load(id); active {
			err := errors.New("server restart is in progress")
			s.log.Warn("server lifecycle operation rejected", "module", module, "server_id", id, "error", err)
			return Record{}, err
		}
	}
	lock := s.lock(id)
	lock.Lock()
	// The lock is released before waiting for the exit goroutine.
	record, err := s.refresh(ctx, id)
	if err != nil {
		lock.Unlock()
		s.log.Error("server lifecycle state could not be loaded", "module", module, "server_id", id, "error", err)
		return Record{}, err
	}
	if record.Runtime.CurrentState != StateRunning {
		lock.Unlock()
		err = errors.New("server is not running")
		s.log.Warn("server lifecycle operation rejected by state", "module", module, "server_id", id, "state", record.Runtime.CurrentState, "error", err)
		return Record{}, err
	}
	record.Runtime.CurrentState = StateStopping
	_ = s.store.SaveRuntime(ctx, id, record.Runtime)
	identity := runtime.Identity{PID: record.Runtime.PID, StartKey: record.Runtime.processStartKey}
	instance, _ := s.instances.Load(id)
	interruptSent := false
	if kill {
		err = s.runtime.Kill(ctx, identity)
	} else if record.Server.StopMethod == "stdin_command" {
		process, ok := instance.(*processInstance)
		if !ok || process.identity != identity {
			err = errors.New("console input is unavailable for the detached process")
		} else {
			err = process.session.Input(record.Server.StopCommand + "\n")
		}
	} else if record.Server.StopMethod == StopMethodConsoleInterrupt {
		process, ok := instance.(*processInstance)
		if !ok || process.identity != identity {
			// A process rediscovered after a GameNode restart has no
			// verifiable, safely addressable console in this GameNode
			// lifetime (see docs/runtime.md). Do not claim a graceful
			// interrupt was attempted; fall back to the existing bounded
			// terminate/force-kill lifecycle instead.
			s.log.Warn("console interrupt unavailable for detached process; falling back to terminate", "module", module, "server_id", id, "pid", identity.PID)
			err = s.runtime.Stop(ctx, identity, time.Duration(record.Server.StopTimeoutSeconds)*time.Second)
		} else {
			err = s.runtime.Interrupt(ctx, identity)
			interruptSent = err == nil
		}
	} else {
		err = s.runtime.Stop(ctx, identity, time.Duration(record.Server.StopTimeoutSeconds)*time.Second)
	}
	if err != nil {
		record.Runtime.CurrentState = StateUnknown
		record.Runtime.LastError = "lifecycle operation failed"
		_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
		lock.Unlock()
		s.log.Error("native server lifecycle operation failed", "module", module, "server_id", id, "pid", identity.PID, "error", err)
		return Record{}, err
	}
	lock.Unlock()
	if process, ok := instance.(*processInstance); ok && process.identity == identity {
		if !kill && (record.Server.StopMethod == "stdin_command" || interruptSent) {
			timer := time.NewTimer(time.Duration(record.Server.StopTimeoutSeconds) * time.Second)
			select {
			case <-process.done:
				timer.Stop()
			case <-timer.C:
				killErr := s.runtime.Kill(context.Background(), identity)
				if killErr != nil && !errors.Is(killErr, runtime.ErrNotRunning) {
					s.log.Error("server force-kill after stop timeout failed", "module", module, "server_id", id, "pid", identity.PID, "error", killErr)
					return Record{}, killErr
				}
				if errors.Is(killErr, runtime.ErrNotRunning) {
					// The game accepted the stop signal and exited, but the Wait
					// callback is delayed by inherited Windows pipe handles. Its
					// identity is already known absent, so finish the managed
					// lifecycle instead of leaving the server permanently stopping.
					s.finalizeInstance(process, runtime.ExitResult{ExitCode: 0})
				}
				// A successful signal delivery is not a successful graceful stop.
				// This is the bounded, controlled record of a timeout fallback;
				// no argv, environment, console content, or handle values.
				s.log.Warn("server did not exit before stop timeout; force-kill fallback used", "module", module, "server_id", id, "pid", identity.PID, "stop_method", record.Server.StopMethod)
				<-process.done
			case <-ctx.Done():
				timer.Stop()
				s.log.Warn("server lifecycle wait cancelled", "module", module, "server_id", id, "error", ctx.Err())
				return Record{}, ctx.Err()
			}
		} else {
			<-process.done
		}
	}
	result, err := s.store.Get(ctx, id)
	if err != nil {
		s.log.Error("final server lifecycle state could not be loaded", "module", module, "server_id", id, "error", err)
		return Record{}, err
	}
	s.log.Info("server lifecycle operation completed", "module", module, "server_id", id, "state", result.Runtime.CurrentState, "restart", restart)
	return result, nil
}
func (s *Service) captureExit(instance *processInstance, exits <-chan runtime.ExitResult) {
	exit, ok := <-exits
	if ok {
		s.finalizeInstance(instance, exit)
	} else {
		s.log.Warn("native process exit channel closed without a result", "module", "Server.Exit", "server_id", instance.serverID, "pid", instance.identity.PID)
	}
}

// finalizeInstance is the sole exit cleanup path. It is idempotent and only
// mutates persisted state if the captured process identity still matches.
func (s *Service) finalizeInstance(instance *processInstance, exit runtime.ExitResult) {
	instance.finalize.Do(func() {
		if exit.Err != nil {
			s.log.Error("native server process exited with an error", "module", "Server.Exit", "server_id", instance.serverID, "pid", instance.identity.PID, "exit_code", exit.ExitCode, "error", exit.Err)
		} else {
			s.log.Info("native server process exited", "module", "Server.Exit", "server_id", instance.serverID, "pid", instance.identity.PID, "exit_code", exit.ExitCode)
		}
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
			if saveErr := s.store.SaveRuntime(context.Background(), instance.serverID, record.Runtime); saveErr != nil {
				s.log.Error("final server process state could not be persisted", "module", "Server.Exit", "server_id", instance.serverID, "error", saveErr)
			}
			s.monitoring.ObserveExit(instance.serverID, instance.identity)
			if record.Runtime.CurrentState == StateCrashed {
				s.scheduleAutoRestart(instance.serverID, instance.instanceID, record.Server)
			}
		} else if err != nil {
			s.log.Error("server process exit could not load persisted state", "module", "Server.Exit", "server_id", instance.serverID, "error", err)
		} else {
			s.log.Warn("stale server process exit ignored", "module", "Server.Exit", "server_id", instance.serverID, "pid", instance.identity.PID)
		}
		s.instances.CompareAndDelete(instance.serverID, instance)
	})
}
func (s *Service) cancelAutoRestart(id string) {
	if value, ok := s.autoRestarts.LoadAndDelete(id); ok {
		value.(*pendingAutoRestart).cancel()
		s.log.Info("pending automatic restart cancelled", "module", "Server.AutoRestart", "server_id", id)
	}
}
func (s *Service) scheduleAutoRestart(id, generation string, server Server) {
	if !server.AutoRestartEnabled {
		s.log.Debug("automatic restart not scheduled because it is disabled", "module", "Server.AutoRestart", "server_id", id)
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
		s.log.Warn("automatic restart limit reached", "module", "Server.AutoRestart", "server_id", id, "attempts", len(kept))
		return
	}
	kept = append(kept, now)
	s.autoAttempts.Store(id, kept)
	ctx, cancel := context.WithCancel(context.Background())
	pending := &pendingAutoRestart{generation: generation, cancel: cancel}
	if old, loaded := s.autoRestarts.Swap(id, pending); loaded {
		old.(*pendingAutoRestart).cancel()
	}
	s.log.Info("automatic server restart scheduled", "module", "Server.AutoRestart", "server_id", id, "attempt", len(kept), "delay_seconds", server.AutoRestartDelaySeconds)
	go func() {
		timer := time.NewTimer(time.Duration(server.AutoRestartDelaySeconds) * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			s.log.Debug("automatic server restart timer cancelled", "module", "Server.AutoRestart", "server_id", id)
			return
		case <-timer.C:
		}
		current, ok := s.autoRestarts.LoadAndDelete(id)
		if !ok || current != pending {
			return
		}
		// The finalizer has already persisted crashed state; this starts a fresh
		// instance/session through the normal orchestration path.
		s.log.Info("automatic server restart starting", "module", "Server.AutoRestart", "server_id", id)
		if _, err := s.start(context.Background(), id, true); err != nil {
			s.log.Error("automatic server restart failed", "module", "Server.AutoRestart", "server_id", id, "error", err)
		}
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
		// Never retain "running" unless the process identity was verified. Keep
		// the identity so a later refresh can resolve the uncertainty, and block
		// a new start so an unverified live process cannot be duplicated.
		record.Runtime.CurrentState = StateUnknown
		record.Runtime.LastError = "process status could not be verified"
		_ = s.store.SaveRuntime(context.Background(), record.Server.ID, record.Runtime)
		return record
	}
	if status.Running {
		record.Runtime.CurrentState = StateRunning
		record.Runtime.ConsoleDetached = true
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
	record.Runtime.ConsoleDetached = false
	_ = s.store.SaveRuntime(context.Background(), record.Server.ID, record.Runtime)
	return record
}
func (s *Service) lock(id string) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}
