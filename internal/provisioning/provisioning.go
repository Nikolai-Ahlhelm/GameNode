package provisioning

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"gamenode/internal/gameconfig"
	"gamenode/internal/ports"
	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
)

const (
	Pending             = "pending"
	Preparing           = "preparing"
	DownloadingSteamCMD = "downloading_steamcmd"
	SteamCMDReady       = "steamcmd_ready"
	Installing          = "installing"
	CreatingServer      = "creating_server"
	Completed           = "completed"
	Failed              = "failed"
	Cancelled           = "cancelled"
)

var (
	ErrNotProvisionable = errors.New("template is not provisionable on this host")
	ErrTargetConflict   = errors.New("server target is already populated or reserved")
	ErrJobNotActive     = errors.New("provisioning job is not active")
	directoryPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type Job struct {
	ID             string     `json:"id"`
	TemplateID     string     `json:"template_id"`
	TemplateName   string     `json:"template_name"`
	ServerName     string     `json:"server_name"`
	DirectoryName  string     `json:"directory_name"`
	InstallerType  string     `json:"installer_type"`
	AppID          int        `json:"app_id"`
	Status         string     `json:"status"`
	Summary        string     `json:"summary"`
	ErrorSummary   string     `json:"error_summary,omitempty"`
	FilesMayRemain bool       `json:"files_may_remain"`
	ServerID       string     `json:"server_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ActorUserID    string     `json:"-"`
	ActorUsername  string     `json:"-"`
}
type Request struct {
	TemplateID, ServerName, DirectoryName string
	Values                                map[string]string
	ActorUserID, ActorUsername            string
}
type Event struct {
	Action   string
	Job      Job
	Duration time.Duration
}
type Provisionability struct {
	Provisionable    bool   `json:"provisionable"`
	HostPlatform     string `json:"host_platform"`
	Summary          string `json:"summary"`
	Installer        string `json:"installer,omitempty"`
	AppID            int    `json:"app_id,omitempty"`
	Validate         bool   `json:"validate"`
	LaunchExecutable string `json:"launch_executable,omitempty"`
}
type Observer func(Event)

type TemplateSource interface {
	Get(context.Context, string) (templates.Template, error)
}
type Installer interface {
	Install(context.Context, string, steamcmd.InstallPlan, io.Writer, steamcmd.EventSink) error
}
type ServerCreator interface {
	CreateProvisioned(context.Context, servers.Server, string, []servers.ProvisionedVariable, []ports.Port, []servers.ProvisionedConfigAdapter) (servers.Record, error)
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db} }
func (s *Store) Create(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO provisioning_jobs(id,actor_user_id,actor_username,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,error_summary,files_may_remain,server_id,created_at,started_at,completed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.ActorUserID, j.ActorUsername, j.TemplateID, j.TemplateName, j.ServerName, j.DirectoryName, j.InstallerType, j.AppID, j.Status, j.Summary, j.ErrorSummary, j.FilesMayRemain, nil, stamp(j.CreatedAt), nil, nil, stamp(j.UpdatedAt))
	return err
}
func (s *Store) Update(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `UPDATE provisioning_jobs SET status=?,summary=?,error_summary=?,files_may_remain=?,server_id=?,started_at=?,completed_at=?,updated_at=? WHERE id=?`, j.Status, j.Summary, j.ErrorSummary, j.FilesMayRemain, nullable(j.ServerID), nullableTime(j.StartedAt), nullableTime(j.CompletedAt), stamp(j.UpdatedAt), j.ID)
	return err
}
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	var j Job
	var created, updated string
	var started, completed, server sql.NullString
	var remains int
	err := s.db.QueryRowContext(ctx, `SELECT id,actor_user_id,actor_username,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,error_summary,files_may_remain,server_id,created_at,started_at,completed_at,updated_at FROM provisioning_jobs WHERE id=?`, id).Scan(&j.ID, &j.ActorUserID, &j.ActorUsername, &j.TemplateID, &j.TemplateName, &j.ServerName, &j.DirectoryName, &j.InstallerType, &j.AppID, &j.Status, &j.Summary, &j.ErrorSummary, &remains, &server, &created, &started, &completed, &updated)
	if err != nil {
		return Job{}, err
	}
	j.FilesMayRemain = remains != 0
	j.ServerID = server.String
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	j.StartedAt = parseTime(started)
	j.CompletedAt = parseTime(completed)
	return j, nil
}
func (s *Store) InterruptActive(ctx context.Context) error {
	now := stamp(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `UPDATE provisioning_jobs SET status='failed',summary='Provisioning was interrupted by a GameNode restart',error_summary='GameNode restarted during provisioning; target files may remain',files_may_remain=1,completed_at=?,updated_at=? WHERE status IN ('pending','preparing','downloading_steamcmd','steamcmd_ready','installing','creating_server')`, now, now)
	return err
}

type Service struct {
	store      *Store
	templates  TemplateSource
	installer  Installer
	servers    ServerCreator
	serverBase string
	hostOS     string
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	wg         sync.WaitGroup
	closed     bool
	active     map[string]*run
	roots      map[string]string
	observer   Observer
	now        func() time.Time
	log        *slog.Logger
}
type run struct {
	cancel context.CancelFunc
	once   sync.Once
	job    Job
	root   string
	// finalizing closes cancellation before the transactional server insert.
	finalizing bool
}
type Options struct {
	HostOS string
	Log    *slog.Logger
}

func New(db *sql.DB, source TemplateSource, installer Installer, creator ServerCreator, dataDirectory string) *Service {
	return NewWithOptions(db, source, installer, creator, dataDirectory, Options{})
}
func NewWithOptions(db *sql.DB, source TemplateSource, installer Installer, creator ServerCreator, dataDirectory string, options Options) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	host := options.HostOS
	if host == "" {
		host = runtime.GOOS
	}
	log := options.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{store: NewStore(db), templates: source, installer: installer, servers: creator, serverBase: filepath.Join(filepath.Clean(dataDirectory), "servers"), hostOS: host, ctx: ctx, cancel: cancel, active: map[string]*run{}, roots: map[string]string{}, now: func() time.Time { return time.Now().UTC() }, log: log}
}
func (s *Service) SetObserver(observer Observer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = observer
}
func (s *Service) Initialize(ctx context.Context) error { return s.store.InterruptActive(ctx) }
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	for _, current := range s.active {
		current.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Service) Start(ctx context.Context, request Request) (Job, error) {
	if strings.TrimSpace(request.ActorUserID) == "" {
		return Job{}, errors.New("provisioning actor is required")
	}
	if !directoryPattern.MatchString(request.DirectoryName) || request.DirectoryName == "." || request.DirectoryName == ".." {
		return Job{}, errors.New("directory name must be a safe relative name")
	}
	template, err := s.templates.Get(ctx, request.TemplateID)
	if err != nil {
		return Job{}, err
	}
	values, sensitive, err := templates.ResolveValues(template, request.Values)
	if err != nil {
		return Job{}, err
	}
	plan, err := CheckProvisionable(template, values, s.hostOS)
	if err != nil {
		return Job{}, err
	}
	launch, ok := templates.LaunchForPlatform(template, s.hostOS)
	if !ok {
		return Job{}, ErrNotProvisionable
	}
	provisionedPorts, err := resolveTemplatePorts(template.Ports, values)
	if err != nil {
		return Job{}, err
	}
	root := filepath.Join(s.serverBase, request.DirectoryName)
	if !inside(s.serverBase, root) {
		return Job{}, errors.New("server target escapes managed storage")
	}
	if err = targetAvailable(root); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Job{}, errors.New("provisioning service is shutting down")
	}
	if _, exists := s.roots[root]; exists {
		s.mu.Unlock()
		return Job{}, ErrTargetConflict
	}
	id, err := newID()
	if err != nil {
		s.mu.Unlock()
		return Job{}, err
	}
	now := s.now()
	job := Job{ID: id, TemplateID: template.ID, TemplateName: template.Name, ServerName: strings.TrimSpace(request.ServerName), DirectoryName: request.DirectoryName, InstallerType: template.Installer.Type, AppID: plan.AppID, Status: Pending, Summary: "Provisioning is queued", CreatedAt: now, UpdatedAt: now, ActorUserID: request.ActorUserID, ActorUsername: request.ActorUsername}
	if job.ServerName == "" || len(job.ServerName) > 100 {
		s.mu.Unlock()
		return Job{}, errors.New("server name must be 1 to 100 characters")
	}
	if err = s.store.Create(ctx, job); err != nil {
		s.mu.Unlock()
		return Job{}, err
	}
	jobCtx, cancel := context.WithCancel(s.ctx)
	current := &run{cancel: cancel, job: job, root: root}
	s.active[id] = current
	s.roots[root] = id
	s.wg.Add(1)
	s.mu.Unlock()
	go s.execute(jobCtx, current, template, *launch, values, sensitive, provisionedPorts, plan)
	return job, nil
}

func (s *Service) Get(ctx context.Context, id string) (Job, error) { return s.store.Get(ctx, id) }
func (s *Service) Check(ctx context.Context, templateID string) (Provisionability, error) {
	template, err := s.templates.Get(ctx, templateID)
	if err != nil {
		return Provisionability{}, err
	}
	values := make(map[string]string, len(template.Variables))
	for _, variable := range template.Variables {
		values[variable.Key] = variable.DefaultValue
	}
	plan, err := CheckProvisionable(template, values, s.hostOS)
	if err != nil {
		return Provisionability{Provisionable: false, HostPlatform: s.hostOS, Summary: "Template cannot be safely installed and launched on this host platform"}, nil
	}
	launch, _ := templates.LaunchForPlatform(template, s.hostOS)
	return Provisionability{Provisionable: true, HostPlatform: s.hostOS, Summary: "Native SteamCMD installation and structured launch are available", Installer: templates.InstallerSteamCMD, AppID: plan.AppID, Validate: plan.Validate, LaunchExecutable: launch.Executable}, nil
}
func (s *Service) Cancel(ctx context.Context, id, actorID string) (Job, error) {
	s.mu.Lock()
	current, ok := s.active[id]
	if !ok || current.job.ActorUserID != actorID || current.finalizing {
		s.mu.Unlock()
		return Job{}, ErrJobNotActive
	}
	current.cancel()
	s.mu.Unlock()
	s.finish(current, Cancelled, "Provisioning was cancelled", "", true, "")
	return s.store.Get(ctx, id)
}

func (s *Service) execute(ctx context.Context, current *run, template templates.Template, launch templates.LaunchDefinition, values map[string]string, sensitive map[string]bool, provisionedPorts []ports.Port, plan steamcmd.InstallPlan) {
	defer s.wg.Done()
	defer s.release(current)
	if ctx.Err() != nil {
		s.finish(current, Cancelled, "Provisioning was cancelled", "", false, "")
		return
	}
	s.phase(current, Preparing, "Preparing managed server storage")
	created, err := prepareRoot(current.root, current.job.ID)
	if err != nil {
		s.log.With("module", "Provisioning.Prepare").Error("managed server storage could not be prepared", "job_id", current.job.ID, "template_id", template.ID)
		s.finish(current, Failed, "Provisioning failed", "Server target could not be prepared", false, "")
		return
	}
	if ctx.Err() != nil {
		s.finish(current, Cancelled, "Provisioning was cancelled", "", created, "")
		return
	}
	s.log.With("module", "SteamCMD.Install").Info("SteamCMD installation started", "job_id", current.job.ID, "template_id", template.ID, "app_id", plan.AppID)
	err = s.installer.Install(ctx, current.root, plan, discardWriter{}, func(event steamcmd.Event) {
		s.log.With("module", "SteamCMD.Install").Info(event.Summary, "job_id", current.job.ID, "template_id", template.ID, "app_id", plan.AppID, "phase", event.Phase)
		switch event.Phase {
		case "downloading_steamcmd":
			s.phase(current, DownloadingSteamCMD, event.Summary)
		case "steamcmd_ready":
			s.phase(current, SteamCMDReady, event.Summary)
		case "installing":
			s.phase(current, Installing, event.Summary)
		}
	})
	if ctx.Err() != nil {
		s.finish(current, Cancelled, "Provisioning was cancelled", "", true, "")
		return
	}
	if err != nil {
		s.log.With("module", "SteamCMD.Install").Error("SteamCMD installation failed", "job_id", current.job.ID, "template_id", template.ID, "app_id", plan.AppID, "failure", steamCMDFailure(err))
		s.finish(current, Failed, "Game installation failed", "SteamCMD could not install the game; target files may remain", true, "")
		return
	}
	s.phase(current, CreatingServer, "Finalizing configuration and creating the GameNode server")
	configSnapshots := make([]servers.ProvisionedConfigAdapter, 0, len(template.ResolvedAdapters))
	for _, adapter := range template.ResolvedAdapters {
		adapterValues := map[string]string{}
		for _, field := range adapter.Fields {
			if value, ok := values[field.Key]; ok {
				adapterValues[field.Key] = value
			}
		}
		if !adapter.PostStartOnly {
			if err = gameconfig.Apply(current.root, adapter, adapterValues); err != nil {
				s.log.With("module", "Provisioning.GameConfig").Error("game configuration could not be written", "job_id", current.job.ID, "template_id", template.ID, "adapter_id", adapter.ID)
				s.finish(current, Failed, "Game configuration failed", "Installed files remain but the validated game configuration could not be written", true, "")
				return
			}
		}
		definitionJSON, marshalErr := json.Marshal(adapter)
		if marshalErr != nil {
			s.log.With("module", "Provisioning.GameConfig").Error("game configuration snapshot could not be created", "job_id", current.job.ID, "template_id", template.ID, "adapter_id", adapter.ID)
			s.finish(current, Failed, "Game configuration failed", "Configuration snapshot could not be created", true, "")
			return
		}
		configSnapshots = append(configSnapshots, servers.ProvisionedConfigAdapter{ID: adapter.ID, SchemaVersion: adapter.SchemaVersion, Version: adapter.Version, TemplateID: template.ID, TemplateVersion: template.Version, DefinitionJSON: definitionJSON})
	}
	server, metadata, err := buildServer(template, launch, current.job.ServerName, current.root, values, sensitive)
	if err != nil {
		s.log.With("module", "Provisioning.ServerConfig").Error("server configuration could not be built", "job_id", current.job.ID, "template_id", template.ID)
		s.finish(current, Failed, "Server configuration failed", "Installed files remain but no GameNode server was created", true, "")
		return
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		s.finish(current, Cancelled, "Provisioning was cancelled", "", true, "")
		return
	}
	current.finalizing = true
	s.mu.Unlock()
	record, err := s.servers.CreateProvisioned(ctx, server, template.ID, metadata, provisionedPorts, configSnapshots)
	if err != nil {
		s.log.With("module", "Server.Create").Error("provisioned server could not be created", "job_id", current.job.ID, "template_id", template.ID, "failure", "server_creation_failed")
		s.finish(current, Failed, "Server creation failed", "Installed files remain but no GameNode server was created", true, "")
		return
	}
	_ = os.Remove(filepath.Join(current.root, ".gamenode-provisioning.json"))
	s.finish(current, Completed, "Server installed successfully", "", false, record.Server.ID)
	s.log.With("module", "Provisioning.Complete").Info("server provisioned", "job_id", current.job.ID, "server_id", record.Server.ID, "template_id", template.ID, "app_id", plan.AppID)
}

func steamCMDFailure(err error) string {
	switch {
	case errors.Is(err, steamcmd.ErrInstallFailed):
		return "install_failed"
	case errors.Is(err, steamcmd.ErrManagedInstallCorrupt):
		return "managed_install_corrupt"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "bootstrap_or_install_unavailable"
	}
}

func (s *Service) phase(current *run, status, summary string) {
	s.mu.Lock()
	if current.job.Status == Completed || current.job.Status == Failed || current.job.Status == Cancelled {
		s.mu.Unlock()
		return
	}
	current.job.Status = status
	current.job.Summary = summary
	now := s.now()
	if current.job.StartedAt == nil {
		current.job.StartedAt = &now
	}
	current.job.UpdatedAt = now
	job := current.job
	_ = s.store.Update(context.Background(), job)
	s.mu.Unlock()
}
func (s *Service) finish(current *run, status, summary, errorSummary string, files bool, serverID string) {
	current.once.Do(func() {
		s.mu.Lock()
		now := s.now()
		current.job.Status = status
		current.job.Summary = summary
		current.job.ErrorSummary = errorSummary
		current.job.FilesMayRemain = files
		current.job.ServerID = serverID
		current.job.CompletedAt = &now
		current.job.UpdatedAt = now
		job := current.job
		observer := s.observer
		s.mu.Unlock()
		_ = s.store.Update(context.Background(), job)
		if observer != nil {
			action := map[string]string{Completed: "server.provision_complete", Failed: "server.provision_fail", Cancelled: "server.provision_cancel"}[status]
			observer(Event{action, job, now.Sub(job.CreatedAt)})
		}
	})
}
func (s *Service) release(current *run) {
	s.mu.Lock()
	delete(s.active, current.job.ID)
	delete(s.roots, current.root)
	s.mu.Unlock()
}

func CheckProvisionable(template templates.Template, values map[string]string, hostOS string) (steamcmd.InstallPlan, error) {
	if template.Compatibility.Status == templates.Unsupported || template.Installer.Type != templates.InstallerSteamCMD || template.Installer.SteamCMD == nil {
		return steamcmd.InstallPlan{}, ErrNotProvisionable
	}
	if template.Configuration != nil && len(template.ResolvedAdapters) != len(template.Configuration.Adapters) {
		return steamcmd.InstallPlan{}, ErrNotProvisionable
	}
	launch, ok := templates.LaunchForPlatform(template, hostOS)
	if !ok {
		return steamcmd.InstallPlan{}, ErrNotProvisionable
	}
	source := template.Installer.SteamCMD
	if source.AppID <= 0 || source.InstallTarget != "server_root" || source.LoginMode == "credentials_required" {
		return steamcmd.InstallPlan{}, ErrNotProvisionable
	}
	for _, key := range []string{source.UsernameVariable, source.PasswordVariable, source.AuthVariable, source.BetaPasswordVariable} {
		if key != "" && values[key] != "" {
			return steamcmd.InstallPlan{}, ErrNotProvisionable
		}
	}
	if (source.Platform == "windows" && hostOS != "windows") || (source.Platform == "linux" && hostOS != "linux") {
		return steamcmd.InstallPlan{}, ErrNotProvisionable
	}
	if len(template.PlatformLaunches) == 0 {
		executable := strings.ToLower(launch.Executable)
		if hostOS == "windows" && !strings.HasSuffix(executable, ".exe") {
			return steamcmd.InstallPlan{}, ErrNotProvisionable
		}
		if hostOS == "linux" && strings.HasSuffix(executable, ".exe") {
			return steamcmd.InstallPlan{}, ErrNotProvisionable
		}
	}
	known := make(map[string]bool, len(values))
	for key := range values {
		known[key] = true
	}
	if _, err := templates.ExpandRelativePath(launch.Executable, values, known); err != nil {
		return steamcmd.InstallPlan{}, ErrNotProvisionable
	}
	if launch.WorkingDirectory != "" {
		if _, err := templates.ExpandRelativePath(launch.WorkingDirectory, values, known); err != nil {
			return steamcmd.InstallPlan{}, ErrNotProvisionable
		}
	}
	branch := ""
	if source.BetaBranchVariable != "" {
		branch = values[source.BetaBranchVariable]
	}
	plan := steamcmd.InstallPlan{AppID: source.AppID, Validate: source.Validate, BetaBranch: branch, LoginMode: "anonymous"}
	if steamcmd.ValidatePlan(plan) != nil {
		return steamcmd.InstallPlan{}, ErrNotProvisionable
	}
	return plan, nil
}

func buildServer(template templates.Template, launch templates.LaunchDefinition, name, root string, values map[string]string, sensitive map[string]bool) (servers.Server, []servers.ProvisionedVariable, error) {
	known := map[string]bool{}
	for key := range values {
		known[key] = true
		if sensitive[key] && (strings.Contains(launch.Executable, "{{"+key+"}}") || strings.Contains(launch.Executable, "${"+key+"}")) {
			return servers.Server{}, nil, errors.New("sensitive variables are not permitted in a launch executable")
		}
	}
	executable, err := templates.ExpandRelativePath(launch.Executable, values, known)
	if err != nil {
		return servers.Server{}, nil, errors.New("launch executable expansion failed")
	}
	expectedExecutable := filepath.Join(root, filepath.FromSlash(executable))
	resolvedExecutable, err := filepath.EvalSymlinks(expectedExecutable)
	if err != nil || !inside(root, resolvedExecutable) {
		return servers.Server{}, nil, errors.New("expected launch executable is missing or unsafe")
	}
	executableInfo, err := os.Stat(resolvedExecutable)
	if err != nil || !executableInfo.Mode().IsRegular() {
		return servers.Server{}, nil, errors.New("expected launch executable is missing or unsafe")
	}
	workingDirectory := root
	if launch.WorkingDirectory != "" {
		relativeWorkingDirectory, expandErr := templates.ExpandRelativePath(launch.WorkingDirectory, values, known)
		if expandErr != nil {
			return servers.Server{}, nil, errors.New("working directory expansion failed")
		}
		workingDirectory, err = filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relativeWorkingDirectory)))
		if err != nil || !inside(root, workingDirectory) {
			return servers.Server{}, nil, errors.New("working directory is missing or unsafe")
		}
		workingInfo, statErr := os.Stat(workingDirectory)
		if statErr != nil || !workingInfo.IsDir() {
			return servers.Server{}, nil, errors.New("working directory is missing or unsafe")
		}
	}
	arguments := make([]string, 0, len(launch.Arguments))
	for _, raw := range launch.Arguments {
		value, err := templates.Expand(raw, values, known)
		if err != nil {
			return servers.Server{}, nil, errors.New("launch argument expansion failed")
		}
		arguments = append(arguments, value)
	}
	metadata := make([]servers.ProvisionedVariable, 0, len(values))
	environment := map[string]string{}
	for key, value := range values {
		environment[key] = value
		metadata = append(metadata, servers.ProvisionedVariable{Key: key, Sensitive: sensitive[key], Source: template.SourceType, Version: template.Version})
	}
	stopMethod := launch.StopMethod
	if stopMethod == "" {
		stopMethod = "terminate"
	}
	stopTimeout := launch.StopTimeout
	if stopTimeout == 0 {
		stopTimeout = 15
	}
	server := servers.Server{CreationMode: servers.CreationTemplate, Name: name, Description: template.Description, WorkingDirectory: workingDirectory, Executable: resolvedExecutable, Arguments: arguments, EnvironmentVariables: environment, RuntimeType: "native", RestartPolicy: "never", StopMethod: stopMethod, StopCommand: launch.StopCommand, StopTimeoutSeconds: stopTimeout, AutoRestartMaxAttempts: 3, AutoRestartWindowSeconds: 300, AutoRestartDelaySeconds: 5}
	if err = server.Validate(); err != nil {
		return servers.Server{}, nil, errors.New("installed server definition is invalid")
	}
	return server, metadata, nil
}

func resolveTemplatePorts(definitions []templates.TemplatePort, values map[string]string) ([]ports.Port, error) {
	result := make([]ports.Port, 0, len(definitions))
	for _, definition := range definitions {
		value := definition.Port
		if definition.Variable != "" {
			parsed, err := strconv.Atoi(values[definition.Variable])
			if err != nil {
				return nil, errors.New("template port value is invalid")
			}
			value = parsed + definition.Offset
		}
		candidate := ports.Port{Name: definition.Name, Protocol: definition.Protocol, Port: value}
		if err := ports.Validate(&candidate); err != nil {
			return nil, errors.New("template port value is invalid")
		}
		for _, existing := range result {
			if ports.Conflict(candidate, existing) {
				return nil, errors.New("template ports conflict")
			}
		}
		result = append(result, candidate)
	}
	return result, nil
}

func targetAvailable(root string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("server target is unavailable")
	}
	if len(entries) > 0 {
		return ErrTargetConflict
	}
	return nil
}
func prepareRoot(root, jobID string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return false, err
	}
	created := false
	if _, err := os.Stat(root); os.IsNotExist(err) {
		if err = os.Mkdir(root, 0700); err != nil {
			return false, err
		}
		created = true
	} else if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) > 0 {
		return created, ErrTargetConflict
	}
	marker, _ := json.Marshal(map[string]string{"job_id": jobID, "state": "provisioning"})
	if err = os.WriteFile(filepath.Join(root, ".gamenode-provisioning.json"), marker, 0600); err != nil {
		return created, err
	}
	return true, nil
}
func inside(base, target string) bool {
	relative, err := filepath.Rel(base, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

type discardWriter struct{}

func (discardWriter) Write(value []byte) (int, error) { return len(value), nil }
func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return stamp(*value)
}
func parseTime(value sql.NullString) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}
