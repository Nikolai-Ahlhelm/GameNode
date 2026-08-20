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
	"gamenode/internal/tenants"
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
	// ErrInvalidTenant is returned when a server references a tenant ID that
	// does not exist.
	ErrInvalidTenant              = errors.New("invalid tenant")
	ErrTenantMigrationSame        = errors.New("server already belongs to target tenant")
	ErrTenantMigrationActive      = errors.New("stop the server before migrating it")
	ErrTenantMigrationUnsupported = errors.New("tenant migration requires a provisioned server")
	ErrTenantMigrationStorage     = errors.New("managed server storage migration is unavailable")
	ErrTenantMigrationPath        = errors.New("provisioned server storage path is invalid")
	// ErrUpdateInProgress is returned by BeginUpdate when a manual SteamCMD
	// update (see internal/serverupdates) already reserved this server, and
	// by Start/Delete when they observe that reservation.
	ErrUpdateInProgress = errors.New("server update is in progress")
	// ErrLaunchExecutableMissing is returned by VerifyLaunchExecutablePresent
	// when the persisted launch executable no longer safely exists inside the
	// server's working directory after a SteamCMD update.
	ErrLaunchExecutableMissing = errors.New("launch executable is missing or unsafe after update")
	// ErrDuplicateName is returned by Create/CreateProvisioned, and by
	// NameAvailable, when another server already has this name. `servers.name`
	// is COLLATE NOCASE UNIQUE (migrations/002_servers.sql,
	// migrations/020_tenants.sql), so the comparison is case-insensitive and
	// global across tenants, matching the DB constraint that ultimately backs
	// it.
	ErrDuplicateName = errors.New("a server with this name already exists")
)

type Server struct {
	ID string `json:"id"`
	// TenantID is the server's owning tenant. Every server belongs to
	// exactly one tenant (see internal/tenants and
	// migrations/020_tenants.sql). It defaults to tenants.DefaultTenantID
	// when left empty at creation and remains unchanged through ordinary
	// updates; the explicit administrator migration service is the only move
	// operation.
	TenantID                      string            `json:"tenant_id"`
	CreationMode                  string            `json:"creation_mode"`
	Name                          string            `json:"name"`
	Description                   string            `json:"description"`
	WorkingDirectory              string            `json:"working_directory"`
	Executable                    string            `json:"executable"`
	Arguments                     []string          `json:"arguments"`
	EnvironmentVariables          map[string]string `json:"environment_variables"`
	SensitiveEnvironmentVariables []string          `json:"sensitive_environment_variables,omitempty"`
	RuntimeType                   string            `json:"runtime_type"`
	Container                     *ContainerConfig  `json:"container,omitempty"`
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
	s.TenantID = strings.TrimSpace(s.TenantID)
	if s.TenantID == "" {
		// Matching this function's existing style of defaulting an unset
		// field (CreationMode, RuntimeType, RestartPolicy, StopMethod
		// below), a server with no explicit tenant belongs to the default
		// tenant rather than staying tenantless. This keeps every existing
		// construction path - the direct Create Server API, Adopt Existing,
		// and template/provisioning's buildServer - working unchanged until
		// a later Tenant Foundation step adds real tenant selection.
		s.TenantID = tenants.DefaultTenantID
	}
	if len(s.TenantID) > 100 || strings.ContainsRune(s.TenantID, 0) {
		return errors.New("invalid tenant")
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
	if s.RuntimeType == "" {
		s.RuntimeType = RuntimeNative
	}
	if s.RuntimeType != RuntimeNative && s.RuntimeType != RuntimeContainer {
		return errors.New("runtime type must be native or container")
	}
	if s.RuntimeType == RuntimeContainer {
		if s.Container == nil {
			return errors.New("container configuration is required")
		}
		if err := s.Container.Validate(); err != nil {
			return err
		}
		// A container launches its configured command inside its fixed mount;
		// executable validation applies only to host-native processes.
		s.Executable = ""
	} else if s.Executable == "" || strings.ContainsRune(s.Executable, 0) {
		return errors.New("executable is required")
	}
	resolved := s.ResolvedExecutable()
	if s.RuntimeType == RuntimeNative && !filepath.IsAbs(s.Executable) && !inside(s.WorkingDirectory, resolved) {
		return errors.New("relative executable escapes working directory")
	}
	executableInfo, err := os.Stat(resolved)
	if s.RuntimeType == RuntimeNative && (err != nil || executableInfo.IsDir()) {
		return errors.New("executable must be an existing file")
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

// requireTenant translates a missing tenant into the controlled
// ErrInvalidTenant instead of letting a raw foreign key constraint failure
// reach callers. internal/servers deliberately queries the tenants table
// directly rather than importing internal/tenants' Service, matching the
// existing cross-domain read convention (see identity.ListGroupSummaries).
func (store *Store) requireTenant(ctx context.Context, tenantID string) error {
	var exists int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants WHERE id=?`, tenantID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrInvalidTenant
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

// NameAvailable reports ErrDuplicateName when another server already has
// this name (comparison is case-insensitive, matching the `servers.name`
// COLLATE NOCASE UNIQUE column - see ErrDuplicateName). It is a best-effort,
// point-in-time check with no reservation: Create and CreateProvisioned
// remain the authoritative gate, since a concurrent insert can still win the
// race between this check and their own insert.
func (store *Store) NameAvailable(ctx context.Context, name string) error {
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE name=?`, strings.TrimSpace(name)).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrDuplicateName
	}
	return nil
}

func (store *Store) Create(ctx context.Context, server Server) (Record, error) {
	if err := server.Validate(); err != nil {
		return Record{}, err
	}
	if err := store.requireTenant(ctx, server.TenantID); err != nil {
		return Record{}, err
	}
	if err := store.NameAvailable(ctx, server.Name); err != nil {
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
	_, err = store.db.ExecContext(ctx, `INSERT INTO servers(id,tenant_id,creation_mode,name,description,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,auto_restart_enabled,auto_restart_max_attempts,auto_restart_window_seconds,auto_restart_delay_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, server.ID, server.TenantID, server.CreationMode, server.Name, server.Description, server.WorkingDirectory, server.Executable, string(args), string(env), server.RuntimeType, server.AutoStart, server.RestartPolicy, server.StopMethod, server.StopCommand, server.StopTimeoutSeconds, server.AutoRestartEnabled, server.AutoRestartMaxAttempts, server.AutoRestartWindowSeconds, server.AutoRestartDelaySeconds, stamp(now), stamp(now))
	if err != nil {
		// classifyNameConstraint is a sanitized safety net for the race
		// window NameAvailable cannot close (two concurrent creates can both
		// pass the check before either inserts): it never lets the raw
		// driver/SQL error reach a caller.
		if classified := classifyNameConstraint(err); classified != nil {
			return Record{}, classified
		}
		return Record{}, fmt.Errorf("create server: %w", err)
	}
	if server.RuntimeType == RuntimeContainer {
		if err = store.saveContainerConfig(ctx, server.ID, server.Container); err != nil {
			return Record{}, err
		}
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

// ProvisionedSteamCMD is the minimum trusted, immutable SteamCMD provenance
// GameNode needs to safely re-run SteamCMD against an already-provisioned
// server's existing root later (see internal/serverupdates). It is written
// exactly once, in the same transaction as the server row, only for servers
// provisioned through the Official SteamCMD installer path; a nil value
// (custom/adopted servers, or any other installer type) leaves the server
// permanently ineligible for a manual update rather than guessed later from
// directory contents or a freshly re-resolved template.
type ProvisionedSteamCMD struct {
	InstallerType   string
	AppID           int
	LoginMode       string
	Validate        bool
	BetaBranch      string
	TemplateID      string
	TemplateVersion string
	TemplateSource  string
}

// CreateProvisioned atomically publishes a fully installed native server and
// its template-variable sensitivity metadata. Filesystem installation has
// already completed and is deliberately outside this database transaction.
func (store *Store) CreateProvisioned(ctx context.Context, server Server, templateID string, variables []ProvisionedVariable, provisionedPorts []ports.Port, configAdapters []ProvisionedConfigAdapter, steamCMD *ProvisionedSteamCMD) (Record, error) {
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
	var tenantExists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants WHERE id=?`, server.TenantID).Scan(&tenantExists); err != nil {
		return Record{}, err
	}
	if tenantExists == 0 {
		return Record{}, ErrInvalidTenant
	}
	var nameCount int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE name=?`, server.Name).Scan(&nameCount); err != nil {
		return Record{}, err
	}
	if nameCount > 0 {
		return Record{}, ErrDuplicateName
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO servers(id,tenant_id,creation_mode,name,description,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,auto_restart_enabled,auto_restart_max_attempts,auto_restart_window_seconds,auto_restart_delay_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, server.ID, server.TenantID, server.CreationMode, server.Name, server.Description, server.WorkingDirectory, server.Executable, string(args), string(env), server.RuntimeType, server.AutoStart, server.RestartPolicy, server.StopMethod, server.StopCommand, server.StopTimeoutSeconds, server.AutoRestartEnabled, server.AutoRestartMaxAttempts, server.AutoRestartWindowSeconds, server.AutoRestartDelaySeconds, stamp(now), stamp(now))
	if err != nil {
		if classified := classifyNameConstraint(err); classified != nil {
			return Record{}, classified
		}
		return Record{}, fmt.Errorf("create provisioned server: %w", err)
	}
	if server.RuntimeType == RuntimeContainer {
		if err = store.saveContainerConfigTx(ctx, tx, server.ID, server.Container); err != nil {
			return Record{}, err
		}
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
	existingRows, err := tx.QueryContext(ctx, `SELECT name,protocol,bind_address,port,container_port FROM server_ports`)
	if err != nil {
		return Record{}, err
	}
	var existingPorts []ports.Port
	for existingRows.Next() {
		var existing ports.Port
		var containerPort sql.NullInt64
		if err = existingRows.Scan(&existing.Name, &existing.Protocol, &existing.BindAddress, &existing.Port, &containerPort); err != nil {
			existingRows.Close()
			return Record{}, err
		}
		if containerPort.Valid {
			existing.ContainerPort = int(containerPort.Int64)
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO server_ports(id,server_id,name,protocol,bind_address,port,container_port,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, portID, server.ID, provisionedPort.Name, provisionedPort.Protocol, provisionedPort.BindAddress, provisionedPort.Port, nullableContainerPort(provisionedPort.ContainerPort), stamp(now), stamp(now)); err != nil {
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
	if steamCMD != nil {
		if steamCMD.AppID <= 0 || steamCMD.LoginMode != "anonymous" || steamCMD.TemplateID != templateID {
			return Record{}, errors.New("invalid provisioned steamcmd metadata")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO server_steamcmd_provisioning(server_id,installer_type,app_id,login_mode,validate_default,beta_branch,template_id,template_version,template_source,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, server.ID, steamCMD.InstallerType, steamCMD.AppID, steamCMD.LoginMode, steamCMD.Validate, steamCMD.BetaBranch, steamCMD.TemplateID, steamCMD.TemplateVersion, steamCMD.TemplateSource, stamp(now)); err != nil {
			return Record{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Record{}, err
	}
	return Record{Server: server, Runtime: RuntimeState{CurrentState: StateStopped}}, nil
}

// SteamCMDProvisioning returns the trusted SteamCMD provenance persisted for
// a server at provisioning time, if any. The bool result is false when no
// row exists (custom/adopted servers, non-SteamCMD templates, or servers
// provisioned before this metadata existed) — callers must treat that as
// "not eligible for a manual update", never fall back to guessing from the
// template catalog or directory contents.
func (store *Store) SteamCMDProvisioning(ctx context.Context, serverID string) (ProvisionedSteamCMD, bool, error) {
	var info ProvisionedSteamCMD
	var validate int
	err := store.db.QueryRowContext(ctx, `SELECT installer_type,app_id,login_mode,validate_default,beta_branch,template_id,template_version,template_source FROM server_steamcmd_provisioning WHERE server_id=?`, serverID).Scan(&info.InstallerType, &info.AppID, &info.LoginMode, &validate, &info.BetaBranch, &info.TemplateID, &info.TemplateVersion, &info.TemplateSource)
	if errors.Is(err, sql.ErrNoRows) {
		return ProvisionedSteamCMD{}, false, nil
	}
	if err != nil {
		return ProvisionedSteamCMD{}, false, err
	}
	info.Validate = validate != 0
	return info, true, nil
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
		if err = store.hydrateContainer(ctx, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
func (store *Store) Get(ctx context.Context, id string) (Record, error) {
	row := store.db.QueryRowContext(ctx, selectSQL+` WHERE s.id=?`, id)
	record, err := scan(row)
	if err != nil {
		return Record{}, err
	}
	if err = store.hydrateContainer(ctx, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (store *Store) hydrateContainer(ctx context.Context, record *Record) error {
	if record.Server.RuntimeType != RuntimeContainer {
		return nil
	}
	config, err := store.containerConfig(ctx, record.Server.ID)
	if err != nil {
		return err
	}
	if config == nil {
		return errors.New("container configuration is missing")
	}
	record.Server.Container = config
	return nil
}
func (store *Store) Update(ctx context.Context, id string, server Server) (Record, error) {
	existing, err := store.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	server.ID = id
	server.CreatedAt = existing.Server.CreatedAt
	// Ordinary edits never change tenant ownership; the explicit migration
	// operation has its own lifecycle and authorization path.
	server.TenantID = existing.Server.TenantID
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
	if server.RuntimeType == RuntimeContainer {
		if err = store.saveContainerConfig(ctx, id, server.Container); err != nil {
			return Record{}, err
		}
	}
	return store.Get(ctx, id)
}

// MigrateTenant persists the result of a completed managed-storage move. The
// filesystem operation is performed by Service.MigrateTenant before this
// method is called; keeping the database update separate lets the service
// attempt a filesystem rollback if persistence fails.
func (store *Store) MigrateTenant(ctx context.Context, id, tenantID, workingDirectory, executable string) (Record, error) {
	existing, err := store.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return Record{}, ErrInvalidTenant
	}
	if existing.Server.TenantID == tenantID {
		return Record{}, ErrTenantMigrationSame
	}
	updated := time.Now().UTC()
	result, err := store.db.ExecContext(ctx, `UPDATE servers SET tenant_id=?,working_directory=?,executable=?,updated_at=? WHERE id=?`, tenantID, workingDirectory, executable, stamp(updated), id)
	if err != nil {
		return Record{}, err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return Record{}, rowsErr
	} else if affected == 0 {
		return Record{}, sql.ErrNoRows
	}
	existing.Server.TenantID = tenantID
	existing.Server.WorkingDirectory = workingDirectory
	existing.Server.Executable = executable
	existing.Server.UpdatedAt = updated
	return existing, nil
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

const selectSQL = `SELECT s.id,s.tenant_id,s.creation_mode,s.name,s.description,s.working_directory,s.executable,s.arguments_json,s.environment_json,s.runtime_type,s.auto_start,s.restart_policy,s.stop_method,s.stop_command,s.stop_timeout_seconds,s.auto_restart_enabled,s.auto_restart_max_attempts,s.auto_restart_window_seconds,s.auto_restart_delay_seconds,s.created_at,s.updated_at,r.pid,r.process_start_key,r.process_started_at,r.last_start_at,r.last_stop_at,r.last_exit_at,r.exit_code,r.last_crash_at,r.crash_count,r.restart_count,r.last_error,r.current_state FROM servers s JOIN server_runtime_state r ON r.server_id=s.id`

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
	err := row.Scan(&r.Server.ID, &r.Server.TenantID, &r.Server.CreationMode, &r.Server.Name, &r.Server.Description, &r.Server.WorkingDirectory, &r.Server.Executable, &args, &env, &r.Server.RuntimeType, &auto, &r.Server.RestartPolicy, &r.Server.StopMethod, &r.Server.StopCommand, &r.Server.StopTimeoutSeconds, &autoRestart, &r.Server.AutoRestartMaxAttempts, &r.Server.AutoRestartWindowSeconds, &r.Server.AutoRestartDelaySeconds, &created, &updated, &pid, &key, &processStart, &lastStart, &lastStop, &lastExit, &exit, &lastCrash, &r.Runtime.CrashCount, &r.Runtime.RestartCount, &r.Runtime.LastError, &r.Runtime.CurrentState)
	if err != nil {
		return Record{}, err
	}
	r.Server.AutoStart = auto != 0
	r.Server.AutoRestartEnabled = autoRestart != 0
	_ = json.Unmarshal([]byte(args), &r.Server.Arguments)
	_ = json.Unmarshal([]byte(env), &r.Server.EnvironmentVariables)
	if r.Server.RuntimeType == RuntimeContainer {
		// This helper cannot receive a context/database; Get/List hydrate below.
		r.Server.Container = nil
	}
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

// classifyNameConstraint recognizes the `servers.name` UNIQUE constraint in a
// raw insert error and maps it to the sanitized ErrDuplicateName, returning
// nil for any other error so the caller falls back to its normal wrapping.
// This mirrors internal/tenants' identical classifyConstraint idiom; it
// exists only as a defense-in-depth safety net for the race NameAvailable's
// separate SELECT cannot close, so a raw driver/SQL error can never reach an
// API caller (see docs/security.md).
func classifyNameConstraint(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "constraint") && strings.Contains(message, "servers.name") {
		return ErrDuplicateName
	}
	return nil
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

func nullableContainerPort(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[0:4]) + "-" + hex.EncodeToString(raw[4:6]) + "-4" + hex.EncodeToString(raw[6:8])[1:] + "-a" + hex.EncodeToString(raw[8:10])[1:] + "-" + hex.EncodeToString(raw[10:]), nil
}

type Service struct {
	store           *Store
	runtime         runtime.Runtime
	console         *console.Manager
	monitoring      *monitoring.Service
	ports           *ports.Service
	locks           sync.Map
	instances       sync.Map
	restarts        sync.Map
	deletions       sync.Map
	updates         sync.Map
	autoRestarts    sync.Map
	autoAttempts    sync.Map
	autoMu          sync.Mutex
	pullMu          sync.Mutex
	pulls           map[string]string
	log             *slog.Logger
	launch          LaunchResolver
	observer        func(LifecycleEvent)
	managedDataRoot string
	tenantRootMover interface {
		MoveManagedRoot(dataRoot, source, destination string) error
	}
}

// LifecycleEvent is the bounded, transport-free notification emitted after a
// durable lifecycle outcome. It deliberately contains no launch arguments,
// environment values, console data, or host paths.
type LifecycleEvent struct {
	Type       string
	ServerID   string
	ServerName string
	TenantID   string
	ExitCode   *int
	OccurredAt time.Time
}

const (
	EventStarted           = "started"
	EventStopped           = "stopped"
	EventCrashed           = "crashed"
	EventRestarted         = "restarted"
	EventAutoRestartFailed = "auto_restart_failed"
	EventAutoRestartLimit  = "auto_restart_limit"
)

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

// SetLifecycleObserver connects a non-blocking observer at composition time.
// The observer must return promptly; notification delivery belongs outside
// the lifecycle service.
func (s *Service) SetLifecycleObserver(observer func(LifecycleEvent)) { s.observer = observer }

// SetTenantMigrationStorage wires the filesystem boundary used by the
// administrator-only provisioned-server tenant migration. Keeping this
// dependency as a narrow interface avoids giving the lifecycle service any
// transport or filesystem implementation knowledge.
func (s *Service) SetTenantMigrationStorage(dataRoot string, mover interface {
	MoveManagedRoot(dataRoot, source, destination string) error
}) {
	s.managedDataRoot = filepath.Clean(strings.TrimSpace(dataRoot))
	s.tenantRootMover = mover
}

func (s *Service) observe(event LifecycleEvent) {
	if s.observer != nil {
		s.observer(event)
	}
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
	return &Service{store: store, runtime: r, console: manager, monitoring: monitoring.New(r, options), ports: ports.New(store.db), pulls: make(map[string]string), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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

// NameAvailable exposes Store.NameAvailable through the service so callers
// outside this package (see internal/provisioning's early port-and-name
// preflight) can reuse the same authoritative name-collision check without
// duplicating it.
func (s *Service) NameAvailable(ctx context.Context, name string) error {
	return s.store.NameAvailable(ctx, name)
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
func (s *Service) CreateProvisioned(ctx context.Context, server Server, templateID string, variables []ProvisionedVariable, provisionedPorts []ports.Port, configAdapters []ProvisionedConfigAdapter, steamCMD *ProvisionedSteamCMD) (Record, error) {
	record, err := s.store.CreateProvisioned(ctx, server, templateID, variables, provisionedPorts, configAdapters, steamCMD)
	if err != nil {
		s.log.Error("provisioned server registration failed", "module", "Server.Create", "template_id", templateID, "error", err)
		return Record{}, err
	}
	s.log.Info("provisioned server registered", "module", "Server.Create", "server_id", record.Server.ID, "template_id", templateID, "ports", len(provisionedPorts), "config_adapters", len(configAdapters))
	return record, nil
}

// BeginUpdate reserves a server for a manual SteamCMD update (see
// internal/serverupdates), mirroring the deletions/restarts reservation
// idiom used elsewhere in this service. It fails if a reservation already
// exists; the returned release function must always be called exactly once,
// typically deferred by the caller. Start, start, and Delete all check this
// reservation so a manual update can never race a lifecycle action against
// the same server's files.
func (s *Service) BeginUpdate(id string) (func(), error) {
	if _, loaded := s.updates.LoadOrStore(id, struct{}{}); loaded {
		return nil, ErrUpdateInProgress
	}
	return func() { s.updates.Delete(id) }, nil
}

// VerifyLaunchExecutablePresent re-checks that a server's persisted launch
// executable still safely exists inside its working directory. It reuses
// Server.Validate's exact executable/sandbox rules (resolved path, symlink
// escape rejection via inside, regular-file check) rather than duplicating
// them, so a manual SteamCMD update (internal/serverupdates) can validate
// post-update artifacts without a second sandbox implementation.
func (s *Service) VerifyLaunchExecutablePresent(record Record) error {
	server := record.Server
	resolved := server.ResolvedExecutable()
	if !filepath.IsAbs(server.Executable) && !inside(server.WorkingDirectory, resolved) {
		return ErrLaunchExecutableMissing
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return ErrLaunchExecutableMissing
	}
	return nil
}
func (s *Service) SensitiveEnvironmentKeys(ctx context.Context, id string) ([]string, error) {
	return s.store.SensitiveEnvironmentKeys(ctx, id)
}
func (s *Service) List(ctx context.Context) ([]Record, error) {
	records, err := s.refreshAll(ctx)
	if err != nil {
		return nil, err
	}
	for i := range records {
		s.decorateContainerImage(ctx, &records[i])
	}
	return records, nil
}
func (s *Service) Get(ctx context.Context, id string) (Record, error) {
	record, err := s.refresh(ctx, id)
	if err == nil {
		s.decorateContainerImage(ctx, &record)
	}
	return record, err
}

// PullContainerImage is an explicit Server.Edit preparation action. Start
// never pulls implicitly, so an existing server remains pinned to its chosen
// configured image until an operator deliberately prepares it.
func (s *Service) PullContainerImage(ctx context.Context, id string) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if record.Server.RuntimeType != RuntimeContainer || record.Server.Container == nil {
		return errors.New("server does not use a container runtime")
	}
	manager, ok := s.runtime.(runtime.ImageManager)
	if !ok {
		return errors.New("container engine is unavailable")
	}
	s.pullMu.Lock()
	if s.pulls[id] == "pulling" {
		s.pullMu.Unlock()
		return errors.New("container image pull is already in progress")
	}
	s.pulls[id] = "pulling"
	s.pullMu.Unlock()
	err = manager.PullImage(ctx, record.Server.Container.Image)
	s.pullMu.Lock()
	if err != nil {
		s.pulls[id] = "failed"
	} else {
		s.pulls[id] = "idle"
	}
	s.pullMu.Unlock()
	return err
}

func (s *Service) decorateContainerImage(ctx context.Context, record *Record) {
	if record.Server.RuntimeType != RuntimeContainer || record.Server.Container == nil {
		return
	}
	s.pullMu.Lock()
	pull := s.pulls[record.Server.ID]
	s.pullMu.Unlock()
	if pull == "" {
		pull = "idle"
	}
	record.Server.Container.PullState = pull
	manager, ok := s.runtime.(runtime.ImageManager)
	if !ok {
		record.Server.Container.ImageAvailability = "engine_unavailable"
		return
	}
	available, err := manager.ImageAvailable(ctx, record.Server.Container.Image)
	if err != nil {
		record.Server.Container.ImageAvailability = "engine_unavailable"
	} else if available {
		record.Server.Container.ImageAvailability = "available"
	} else {
		record.Server.Container.ImageAvailability = "missing"
	}
}

// SteamCMDProvisioning exposes the trusted persisted SteamCMD provenance for
// a server (see Store.SteamCMDProvisioning) to other domains, notably
// internal/serverupdates, without giving them direct database access.
func (s *Service) SteamCMDProvisioning(ctx context.Context, id string) (ProvisionedSteamCMD, bool, error) {
	return s.store.SteamCMDProvisioning(ctx, id)
}

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

// MigrateTenant is restricted to the administrative API path. Provisioned
// servers are moved as a complete managed storage tree before the database
// ownership/path update is committed. The server must be stopped so a tenant
// boundary cannot change while lifecycle, console, or file permissions are
// being used against a live process.
func (s *Service) MigrateTenant(ctx context.Context, id, tenantID string) (Record, error) {
	s.log.Info("server tenant migration started", "module", "Server.MigrateTenant", "server_id", id, "target_tenant_id", tenantID)
	if _, updating := s.updates.Load(id); updating {
		return Record{}, ErrUpdateInProgress
	}
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	record, err := s.refresh(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.Runtime.CurrentState == StateRunning || record.Runtime.CurrentState == StateStarting || record.Runtime.CurrentState == StateStopping {
		return Record{}, ErrTenantMigrationActive
	}
	if record.Server.CreationMode != CreationTemplate {
		return Record{}, ErrTenantMigrationUnsupported
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return Record{}, ErrInvalidTenant
	}
	if tenantID == record.Server.TenantID {
		return Record{}, ErrTenantMigrationSame
	}
	if s.tenantRootMover == nil || s.managedDataRoot == "" {
		return Record{}, ErrTenantMigrationStorage
	}
	sourceRoot, destinationRoot, err := managedTenantMigrationRoots(s.managedDataRoot, record.Server, tenantID)
	if err != nil {
		return Record{}, err
	}
	if err := s.tenantRootMover.MoveManagedRoot(s.managedDataRoot, sourceRoot, destinationRoot); err != nil {
		return Record{}, err
	}
	executable := record.Server.Executable
	if filepath.IsAbs(executable) && inside(sourceRoot, filepath.Clean(executable)) {
		relative, relativeErr := filepath.Rel(sourceRoot, filepath.Clean(executable))
		if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			_ = s.tenantRootMover.MoveManagedRoot(s.managedDataRoot, destinationRoot, sourceRoot)
			return Record{}, ErrTenantMigrationPath
		}
		executable = filepath.Join(destinationRoot, relative)
	}
	updated, err := s.store.MigrateTenant(ctx, id, tenantID, destinationRoot, executable)
	if err != nil {
		if rollbackErr := s.tenantRootMover.MoveManagedRoot(s.managedDataRoot, destinationRoot, sourceRoot); rollbackErr != nil {
			s.log.Error("server tenant migration rollback failed", "module", "Server.MigrateTenant", "server_id", id, "error", rollbackErr)
		}
		return Record{}, err
	}
	s.log.Info("server tenant migration completed", "module", "Server.MigrateTenant", "server_id", id, "target_tenant_id", updated.Server.TenantID)
	return updated, nil
}

func managedTenantMigrationRoots(dataRoot string, server Server, targetTenantID string) (string, string, error) {
	directory, err := managedServerDirectoryName(dataRoot, server.TenantID, server.WorkingDirectory)
	if err != nil {
		return "", "", err
	}
	sourceRoot, err := tenants.TenantServerRoot(dataRoot, server.TenantID, directory)
	if err != nil {
		return "", "", ErrTenantMigrationPath
	}
	destinationRoot, err := tenants.TenantServerRoot(dataRoot, targetTenantID, directory)
	if err != nil {
		return "", "", ErrTenantMigrationPath
	}
	return sourceRoot, destinationRoot, nil
}

func managedServerDirectoryName(dataRoot, tenantID, workingDirectory string) (string, error) {
	root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(dataRoot)))
	if err != nil || root == "." || strings.TrimSpace(dataRoot) == "" {
		return "", ErrTenantMigrationPath
	}
	serversRoot := filepath.Join(root, "tenants", tenantID, "servers")
	relative, err := filepath.Rel(serversRoot, filepath.Clean(workingDirectory))
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.Dir(relative) != "." {
		return "", ErrTenantMigrationPath
	}
	resolved, err := tenants.TenantServerRoot(root, tenantID, relative)
	if err != nil || !sameManagedPath(resolved, workingDirectory) {
		return "", ErrTenantMigrationPath
	}
	return relative, nil
}

func sameManagedPath(left, right string) bool {
	left, leftErr := filepath.Abs(filepath.Clean(left))
	right, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}
func (s *Service) Delete(ctx context.Context, id string) error {
	s.log.Info("server deletion started", "module", "Server.Delete", "server_id", id)
	if _, updating := s.updates.Load(id); updating {
		return ErrUpdateInProgress
	}
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
	if _, updating := s.updates.Load(id); updating {
		return Record{}, ErrUpdateInProgress
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
	if _, updating := s.updates.Load(id); updating {
		return Record{}, ErrUpdateInProgress
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
	containerOptions, containerOptionsErr := s.runtimeContainerOptions(ctx, record.Server, id, instanceID)
	if containerOptionsErr != nil {
		cleanup()
		record.Runtime.CurrentState = StateStopped
		record.Runtime.LastError = "container startup could not be resolved"
		_ = s.store.SaveRuntime(context.Background(), id, record.Runtime)
		return Record{}, containerOptionsErr
	}
	identity, exits, err := s.runtime.Start(ctx, runtime.StartOptions{
		RuntimeType:      record.Server.RuntimeType,
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
		Container:               containerOptions,
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
	eventType := EventStarted
	if restart {
		eventType = EventRestarted
	}
	s.observe(LifecycleEvent{Type: eventType, ServerID: id, ServerName: result.Server.Name, TenantID: result.Server.TenantID, OccurredAt: now})
	return result, nil
}

func (s *Service) runtimeContainerOptions(ctx context.Context, server Server, serverID, generation string) (*runtime.ContainerOptions, error) {
	if server.RuntimeType != RuntimeContainer || server.Container == nil {
		return nil, nil
	}
	command := append([]string(nil), server.Container.Command...)
	if server.Container.StartupTemplate != "" {
		command = []string{server.Container.StartupShell, "-lc", server.Container.StartupTemplate}
	}
	if len(command) > 0 {
		var err error
		command, err = expandContainerCommand(command, server.EnvironmentVariables)
		if err != nil {
			return nil, err
		}
	}
	result := &runtime.ContainerOptions{Image: server.Container.Image, Command: command, MemoryLimitBytes: server.Container.MemoryLimitBytes, CPULimitMillis: server.Container.CPULimitMillis, ServerID: serverID, Generation: generation, OwnershipToken: server.Container.OwnershipToken, PIDsLimit: server.Container.PIDsLimit, TmpfsSizeBytes: server.Container.TmpfsSizeBytes}
	registered, err := s.ports.List(ctx, serverID)
	if err != nil {
		return result, err
	}
	for _, port := range registered {
		target := port.ContainerPort
		if target == 0 {
			target = port.Port
		}
		result.Ports = append(result.Ports, runtime.ContainerPort{Protocol: port.Protocol, BindAddress: port.BindAddress, HostPort: port.Port, ContainerPort: target})
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
			_, restarting := s.restarts.Load(instance.serverID)
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
			} else if record.Runtime.CurrentState == StateCrashed {
				s.observe(LifecycleEvent{Type: EventCrashed, ServerID: instance.serverID, ServerName: record.Server.Name, TenantID: record.Server.TenantID, ExitCode: &exit.ExitCode, OccurredAt: now})
			} else if !restarting {
				s.observe(LifecycleEvent{Type: EventStopped, ServerID: instance.serverID, ServerName: record.Server.Name, TenantID: record.Server.TenantID, ExitCode: &exit.ExitCode, OccurredAt: now})
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
		s.observe(LifecycleEvent{Type: EventAutoRestartLimit, ServerID: id, ServerName: server.Name, TenantID: server.TenantID, OccurredAt: now})
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
			s.observe(LifecycleEvent{Type: EventAutoRestartFailed, ServerID: id, ServerName: server.Name, TenantID: server.TenantID, OccurredAt: time.Now().UTC()})
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
