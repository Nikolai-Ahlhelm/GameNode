package provisioning

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
	Pending                 = "pending"
	Preparing               = "preparing"
	DownloadingSteamCMD     = "downloading_steamcmd"
	SteamCMDReady           = "steamcmd_ready"
	Installing              = "installing"
	CreatingServer          = "creating_server"
	Completed               = "completed"
	Failed                  = "failed"
	Cancelled               = "cancelled"
	SteamCMDCompleted       = "steamcmd_completed"
	ValidatingInstallation  = "validating_installation"
	InstallationValidated   = "installation_validated"
	ResolvingLaunch         = "resolving_launch"
	RegisteringServer       = "registering_server"
	ServerRegistered        = "server_registered"
	maxInstallerOutputLines = 1000
	maxInstallerOutputBytes = 256 << 10
	maxInstallerOutputJobs  = 64
)

var (
	ErrNotProvisionable    = errors.New("template is not provisionable on this host")
	ErrTargetConflict      = errors.New("server target is already populated or reserved")
	ErrRecoveryUnavailable = errors.New("installed server cannot be recovered")
	ErrJobNotActive        = errors.New("provisioning job is not active")
	directoryPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type Job struct {
	ID                      string     `json:"id"`
	TemplateID              string     `json:"template_id"`
	TemplateName            string     `json:"template_name"`
	ServerName              string     `json:"server_name"`
	DirectoryName           string     `json:"directory_name"`
	InstallerType           string     `json:"installer_type"`
	AppID                   int        `json:"app_id"`
	Status                  string     `json:"status"`
	CurrentPhase            string     `json:"current_phase"`
	LastSuccessfulPhase     string     `json:"last_successful_phase,omitempty"`
	FailurePhase            string     `json:"failure_phase,omitempty"`
	FailureCode             string     `json:"failure_code,omitempty"`
	InstallationCompleted   bool       `json:"installation_completed"`
	RegistrationRecoverable bool       `json:"registration_recoverable"`
	Summary                 string     `json:"summary"`
	ErrorSummary            string     `json:"error_summary,omitempty"`
	FilesMayRemain          bool       `json:"files_may_remain"`
	ServerID                string     `json:"server_id,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	StartedAt               *time.Time `json:"started_at,omitempty"`
	CompletedAt             *time.Time `json:"completed_at,omitempty"`
	UpdatedAt               time.Time  `json:"updated_at"`
	ActorUserID             string     `json:"-"`
	ActorUsername           string     `json:"-"`
	InstallerOutput         []string   `json:"installer_output,omitempty"`
	OutputTruncated         bool       `json:"output_truncated,omitempty"`
	Events                  []JobEvent `json:"events,omitempty"`
}
type JobEvent struct {
	OccurredAt time.Time `json:"occurred_at"`
	Phase      string    `json:"phase"`
	Code       string    `json:"code"`
	Summary    string    `json:"summary"`
}
type Request struct {
	TemplateID, ServerName, DirectoryName string
	Values                                map[string]string
	ActorUserID, ActorUsername            string
	RecoverExisting                       bool
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO provisioning_jobs(id,actor_user_id,actor_username,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,error_summary,files_may_remain,server_id,created_at,started_at,completed_at,updated_at,current_phase,last_successful_phase,failure_phase,failure_code,installation_completed,registration_recoverable) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.ActorUserID, j.ActorUsername, j.TemplateID, j.TemplateName, j.ServerName, j.DirectoryName, j.InstallerType, j.AppID, j.Status, j.Summary, j.ErrorSummary, j.FilesMayRemain, nil, stamp(j.CreatedAt), nil, nil, stamp(j.UpdatedAt), j.CurrentPhase, j.LastSuccessfulPhase, j.FailurePhase, j.FailureCode, j.InstallationCompleted, j.RegistrationRecoverable)
	return err
}
func (s *Store) Update(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `UPDATE provisioning_jobs SET status=?,summary=?,error_summary=?,files_may_remain=?,server_id=?,started_at=?,completed_at=?,updated_at=?,current_phase=?,last_successful_phase=?,failure_phase=?,failure_code=?,installation_completed=?,registration_recoverable=? WHERE id=?`, j.Status, j.Summary, j.ErrorSummary, j.FilesMayRemain, nullable(j.ServerID), nullableTime(j.StartedAt), nullableTime(j.CompletedAt), stamp(j.UpdatedAt), j.CurrentPhase, j.LastSuccessfulPhase, j.FailurePhase, j.FailureCode, j.InstallationCompleted, j.RegistrationRecoverable, j.ID)
	return err
}
func (s *Store) SaveRegistrationSnapshot(ctx context.Context, id string, snapshot []byte) error {
	if len(snapshot) == 0 || len(snapshot) > 256<<10 || !json.Valid(snapshot) {
		return errors.New("invalid registration snapshot")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE provisioning_jobs SET registration_snapshot_json=? WHERE id=?`, string(snapshot), id)
	return err
}
func (s *Store) RegistrationSnapshot(ctx context.Context, id string) ([]byte, error) {
	var value string
	if err := s.db.QueryRowContext(ctx, `SELECT registration_snapshot_json FROM provisioning_jobs WHERE id=?`, id).Scan(&value); err != nil {
		return nil, err
	}
	if value == "" || len(value) > 256<<10 || !json.Valid([]byte(value)) {
		return nil, ErrRecoveryUnavailable
	}
	return []byte(value), nil
}
func (s *Store) Event(ctx context.Context, id, phase, code, summary string, at time.Time) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO provisioning_job_events(job_id,occurred_at,phase,code,summary) SELECT ?,?,?,?,? WHERE (SELECT COUNT(*) FROM provisioning_job_events WHERE job_id=?) < 200`, id, stamp(at), phase, code, summary, id)
}
func (s *Store) Events(ctx context.Context, id string) ([]JobEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT occurred_at,phase,code,summary FROM provisioning_job_events WHERE job_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []JobEvent
	for rows.Next() {
		var event JobEvent
		var at string
		if err = rows.Scan(&at, &event.Phase, &event.Code, &event.Summary); err != nil {
			return nil, err
		}
		event.OccurredAt, _ = time.Parse(time.RFC3339Nano, at)
		events = append(events, event)
	}
	return events, rows.Err()
}
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	var j Job
	var created, updated string
	var started, completed, server sql.NullString
	var remains int
	err := s.db.QueryRowContext(ctx, `SELECT id,actor_user_id,actor_username,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,error_summary,files_may_remain,server_id,created_at,started_at,completed_at,updated_at,current_phase,last_successful_phase,failure_phase,failure_code,installation_completed,registration_recoverable FROM provisioning_jobs WHERE id=?`, id).Scan(&j.ID, &j.ActorUserID, &j.ActorUsername, &j.TemplateID, &j.TemplateName, &j.ServerName, &j.DirectoryName, &j.InstallerType, &j.AppID, &j.Status, &j.Summary, &j.ErrorSummary, &remains, &server, &created, &started, &completed, &updated, &j.CurrentPhase, &j.LastSuccessfulPhase, &j.FailurePhase, &j.FailureCode, &j.InstallationCompleted, &j.RegistrationRecoverable)
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
	_, err := s.db.ExecContext(ctx, `UPDATE provisioning_jobs SET status='failed',summary='Provisioning was interrupted by a GameNode restart',error_summary='GameNode restarted during provisioning; target files may remain',files_may_remain=1,failure_phase=current_phase,failure_code='INTERRUPTED',registration_recoverable=installation_completed,completed_at=?,updated_at=? WHERE status IN ('pending','preparing','downloading_steamcmd','steamcmd_ready','installing','creating_server')`, now, now)
	return err
}

type Service struct {
	store       *Store
	templates   TemplateSource
	installer   Installer
	servers     ServerCreator
	serverBase  string
	hostOS      string
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	wg          sync.WaitGroup
	closed      bool
	active      map[string]*run
	roots       map[string]string
	observer    Observer
	now         func() time.Time
	log         *slog.Logger
	outputs     map[string]*installerOutput
	outputOrder []string
}
type run struct {
	cancel context.CancelFunc
	once   sync.Once
	job    Job
	root   string
	// finalizing closes cancellation before the transactional server insert.
	finalizing bool
	recovering bool
}
type registrationSnapshot struct {
	Server         servers.Server                     `json:"server"`
	TemplateID     string                             `json:"template_id"`
	Variables      []servers.ProvisionedVariable      `json:"variables"`
	Ports          []ports.Port                       `json:"ports"`
	ConfigAdapters []servers.ProvisionedConfigAdapter `json:"config_adapters"`
}
type installerOutput struct {
	lines     []string
	pending   string
	bytes     int
	truncated bool
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
	return &Service{store: NewStore(db), templates: source, installer: installer, servers: creator, serverBase: filepath.Join(filepath.Clean(dataDirectory), "servers"), hostOS: host, ctx: ctx, cancel: cancel, active: map[string]*run{}, roots: map[string]string{}, outputs: map[string]*installerOutput{}, now: func() time.Time { return time.Now().UTC() }, log: log}
}
func (s *Service) SetObserver(observer Observer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = observer
}
func (s *Service) Initialize(ctx context.Context) error {
	s.log.Info("provisioning recovery started", "module", "Provisioning.Recovery")
	if err := s.store.InterruptActive(ctx); err != nil {
		s.log.Error("provisioning recovery failed", "module", "Provisioning.Recovery", "error", err)
		return err
	}
	s.log.Info("provisioning recovery completed", "module", "Provisioning.Recovery")
	return nil
}
func (s *Service) Close() {
	s.log.Info("provisioning service shutdown started", "module", "Provisioning.Shutdown")
	s.mu.Lock()
	s.closed = true
	s.cancel()
	for _, current := range s.active {
		current.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.log.Info("provisioning service shutdown completed", "module", "Provisioning.Shutdown")
}

func (s *Service) Start(ctx context.Context, request Request) (Job, error) {
	s.log.Info("provisioning request validation started", "module", "Provisioning.Start", "template_id", request.TemplateID, "actor_user_id", request.ActorUserID, "recover_existing", request.RecoverExisting)
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
	if request.RecoverExisting {
		if err = recoverableTarget(ctx, s.store, root, template.ID, request.DirectoryName, request.ActorUserID); err != nil {
			return Job{}, err
		}
	} else if err = targetAvailable(root); err != nil {
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
	job := Job{ID: id, TemplateID: template.ID, TemplateName: template.Name, ServerName: strings.TrimSpace(request.ServerName), DirectoryName: request.DirectoryName, InstallerType: template.Installer.Type, AppID: plan.AppID, Status: Pending, CurrentPhase: Pending, Summary: "Provisioning is queued", CreatedAt: now, UpdatedAt: now, ActorUserID: request.ActorUserID, ActorUsername: request.ActorUsername}
	if job.ServerName == "" || len(job.ServerName) > 100 {
		s.mu.Unlock()
		return Job{}, errors.New("server name must be 1 to 100 characters")
	}
	if err = s.store.Create(ctx, job); err != nil {
		s.mu.Unlock()
		s.log.Error("provisioning job could not be persisted", "module", "Provisioning.Start", "template_id", template.ID, "error", err)
		return Job{}, err
	}
	jobCtx, cancel := context.WithCancel(s.ctx)
	current := &run{cancel: cancel, job: job, root: root, recovering: request.RecoverExisting}
	s.active[id] = current
	s.roots[root] = id
	s.outputs[id] = &installerOutput{}
	s.outputOrder = append(s.outputOrder, id)
	s.pruneOutputsLocked()
	s.wg.Add(1)
	s.mu.Unlock()
	s.log.Info("provisioning job queued", "module", "Provisioning.Start", "job_id", job.ID, "template_id", job.TemplateID, "app_id", job.AppID)
	go s.execute(jobCtx, current, template, *launch, values, sensitive, provisionedPorts, plan)
	return job, nil
}

func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	job, err := s.store.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	if output := s.outputs[id]; output != nil {
		job.InstallerOutput = append([]string(nil), output.lines...)
		if output.pending != "" {
			job.InstallerOutput = append(job.InstallerOutput, output.pending)
		}
		job.OutputTruncated = output.truncated
	}
	s.mu.Unlock()
	job.Events, _ = s.store.Events(ctx, id)
	return job, nil
}
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
	s.log.Info("provisioning cancellation requested", "module", "Provisioning.Cancel", "job_id", id, "actor_user_id", actorID)
	s.mu.Lock()
	current, ok := s.active[id]
	if !ok || current.job.ActorUserID != actorID || current.finalizing {
		s.mu.Unlock()
		s.log.Warn("provisioning cancellation rejected", "module", "Provisioning.Cancel", "job_id", id, "error", ErrJobNotActive)
		return Job{}, ErrJobNotActive
	}
	current.cancel()
	s.mu.Unlock()
	s.finish(current, Cancelled, "Provisioning was cancelled", "", true, "")
	s.log.Info("provisioning cancellation accepted", "module", "Provisioning.Cancel", "job_id", id)
	return s.store.Get(ctx, id)
}

func (s *Service) execute(ctx context.Context, current *run, template templates.Template, launch templates.LaunchDefinition, values map[string]string, sensitive map[string]bool, provisionedPorts []ports.Port, plan steamcmd.InstallPlan) {
	s.log.Info("provisioning worker started", "module", "Provisioning.Worker", "job_id", current.job.ID, "template_id", template.ID, "recovering", current.recovering)
	defer s.wg.Done()
	defer s.release(current)
	var err error
	if ctx.Err() != nil {
		s.finish(current, Cancelled, "Provisioning was cancelled", "", false, "")
		return
	}
	if current.recovering {
		s.installationCompleted(current)
		s.phase(current, ValidatingInstallation, "Revalidating installed game files")
	} else {
		s.phase(current, Preparing, "Preparing managed server storage")
		created, err := prepareRoot(current.root, current.job.ID)
		if err != nil {
			s.log.With("module", "Provisioning.Prepare").Error("managed server storage could not be prepared", "job_id", current.job.ID, "template_id", template.ID, "error", err)
			s.finish(current, Failed, "Provisioning failed", "Server target could not be prepared", false, "")
			return
		}
		if ctx.Err() != nil {
			s.finish(current, Cancelled, "Provisioning was cancelled", "", created, "")
			return
		}
		s.log.With("module", "SteamCMD.Install").Info("SteamCMD installation started", "job_id", current.job.ID, "template_id", template.ID, "app_id", plan.AppID)
		err = s.installer.Install(ctx, current.root, plan, s.outputWriter(current.job.ID, values, sensitive), func(event steamcmd.Event) {
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
			failureCode, errorSummary := steamCMDFailureCode(err), "SteamCMD could not install the game; target files may remain"
			if s.installerReportedDiskSpace(current.job.ID) {
				failureCode, errorSummary = "STEAMCMD_INSUFFICIENT_DISK_SPACE", "Not enough free disk space to install this server."
			}
			s.log.With("module", "SteamCMD.Install").Error("SteamCMD installation failed", "job_id", current.job.ID, "template_id", template.ID, "app_id", plan.AppID, "failure", steamCMDFailure(err), "error", err)
			s.fail(current, Installing, failureCode, "Game installation failed", errorSummary, true)
			return
		}
		s.phase(current, SteamCMDCompleted, "SteamCMD completed successfully")
		s.installationCompleted(current)
	}
	if !current.recovering {
		s.phase(current, ValidatingInstallation, "Validating installed game files")
	}
	if err = validateInstallation(launch, current.root, values); err != nil {
		s.log.With("module", "Provisioning.Validation").Error("installed game files failed validation", "job_id", current.job.ID, "template_id", template.ID, "error", err)
		s.fail(current, ValidatingInstallation, "EXPECTED_EXECUTABLE_MISSING", "Installation validation failed", "SteamCMD completed, but the expected game executable was not found", true)
		return
	}
	s.phase(current, InstallationValidated, "Game installation validated")
	s.phase(current, ResolvingLaunch, "Resolving the native server launch")
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
				s.log.With("module", "Provisioning.GameConfig").Error("game configuration could not be written", "job_id", current.job.ID, "template_id", template.ID, "adapter_id", adapter.ID, "error", err)
				s.fail(current, ResolvingLaunch, "LAUNCH_RESOLUTION_FAILED", "Game configuration failed", "Installed files remain but the validated game configuration could not be written", true)
				return
			}
		}
		definitionJSON, marshalErr := json.Marshal(adapter)
		if marshalErr != nil {
			s.log.With("module", "Provisioning.GameConfig").Error("game configuration snapshot could not be created", "job_id", current.job.ID, "template_id", template.ID, "adapter_id", adapter.ID, "error", marshalErr)
			s.fail(current, ResolvingLaunch, "LAUNCH_RESOLUTION_FAILED", "Game configuration failed", "Configuration snapshot could not be created", true)
			return
		}
		configSnapshots = append(configSnapshots, servers.ProvisionedConfigAdapter{ID: adapter.ID, SchemaVersion: adapter.SchemaVersion, Version: adapter.Version, TemplateID: template.ID, TemplateVersion: template.Version, DefinitionJSON: definitionJSON})
	}
	server, metadata, err := buildServer(template, launch, current.job.ServerName, current.root, values, sensitive)
	if err != nil {
		s.log.With("module", "Provisioning.ServerConfig").Error("server configuration could not be built", "job_id", current.job.ID, "template_id", template.ID, "error", err)
		s.fail(current, ResolvingLaunch, "LAUNCH_RESOLUTION_FAILED", "Launch resolution failed", "Game files were installed successfully, but GameNode could not resolve the native server launch", true)
		return
	}
	snapshot, marshalErr := json.Marshal(registrationSnapshot{Server: server, TemplateID: template.ID, Variables: metadata, Ports: provisionedPorts, ConfigAdapters: configSnapshots})
	snapshotErr := marshalErr
	if snapshotErr == nil {
		snapshotErr = s.store.SaveRegistrationSnapshot(context.Background(), current.job.ID, snapshot)
	}
	if snapshotErr != nil {
		s.log.Error("registration snapshot could not be persisted", "module", "Provisioning.Registration", "job_id", current.job.ID, "phase", RegisteringServer, "error_code", "SERVER_RELATED_DATA_FAILED", "error", snapshotErr)
		s.fail(current, RegisteringServer, "SERVER_RELATED_DATA_FAILED", "Server registration failed", "Game files were installed successfully, but GameNode could not prepare the server registration", true)
		return
	}
	s.phase(current, RegisteringServer, "Registering the GameNode server")
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
		failure, summary := serverCreationFailure(err)
		s.log.With("module", "Server.Create").Error("provisioned server could not be created", "job_id", current.job.ID, "template_id", template.ID, "failure", failure, "error", err)
		s.fail(current, RegisteringServer, failure, "Server registration failed", summary, true)
		return
	}
	s.phase(current, ServerRegistered, "Server registered successfully")
	_ = os.Remove(filepath.Join(current.root, ".gamenode-provisioning.json"))
	s.finish(current, Completed, "Server installed successfully", "", false, record.Server.ID)
	s.log.With("module", "Provisioning.Complete").Info("server provisioned", "job_id", current.job.ID, "server_id", record.Server.ID, "template_id", template.ID, "app_id", plan.AppID)
}

// RetryRegistration replays only the previously persisted, normalized server
// registration. It never executes SteamCMD and is serialized per job ID.
func (s *Service) RetryRegistration(ctx context.Context, id, actorID string) (Job, error) {
	job, err := s.store.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.ActorUserID != actorID || job.ServerID != "" || !job.InstallationCompleted || !job.RegistrationRecoverable || job.Status != Failed {
		return Job{}, ErrRecoveryUnavailable
	}
	snapshotJSON, err := s.store.RegistrationSnapshot(ctx, id)
	if err != nil {
		return Job{}, err
	}
	var snapshot registrationSnapshot
	if err = json.Unmarshal(snapshotJSON, &snapshot); err != nil || snapshot.TemplateID != job.TemplateID {
		return Job{}, ErrRecoveryUnavailable
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Job{}, ErrJobNotActive
	}
	if _, active := s.active[id]; active {
		s.mu.Unlock()
		return Job{}, ErrJobNotActive
	}
	current := &run{job: job, root: filepath.Join(s.serverBase, job.DirectoryName)}
	s.active[id] = current
	s.mu.Unlock()
	defer s.release(current)
	s.log.Info("retrying persisted server registration", "module", "Provisioning.Retry", "job_id", id, "template_id", job.TemplateID, "phase", RegisteringServer)
	record, err := s.servers.CreateProvisioned(ctx, snapshot.Server, snapshot.TemplateID, snapshot.Variables, snapshot.Ports, snapshot.ConfigAdapters)
	if err != nil {
		code, summary := serverCreationFailure(err)
		s.log.Error("retry server registration failed", "module", "Provisioning.Retry", "job_id", id, "template_id", job.TemplateID, "phase", RegisteringServer, "error_code", code, "error", err.Error())
		return job, fmt.Errorf("%w: %s", ErrRecoveryUnavailable, summary)
	}
	s.finish(current, Completed, "Server registered successfully", "", false, record.Server.ID)
	return s.store.Get(ctx, id)
}

func serverCreationFailure(err error) (string, string) {
	if errors.Is(err, servers.ErrProvisionedPortConflict) {
		return "SERVER_RELATED_DATA_FAILED", "Game files were installed successfully, but one or more selected ports are already assigned to another GameNode server"
	}
	if errors.Is(err, servers.ErrProvisionedConfigAdapter) {
		return "SERVER_RELATED_DATA_FAILED", "Game files were installed successfully, but GameNode could not save the managed game configuration"
	}
	return "SERVER_DB_CREATE_FAILED", "Game files were installed successfully, but GameNode could not save the server definition"
}

func steamCMDFailureCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "STEAMCMD_CANCELLED"
	}
	if errors.Is(err, steamcmd.ErrInstallFailed) {
		return "STEAMCMD_PROCESS_FAILED"
	}
	return "STEAMCMD_BOOTSTRAP_FAILED"
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
	current.job.CurrentPhase = status
	current.job.LastSuccessfulPhase = status
	current.job.Summary = summary
	now := s.now()
	if current.job.StartedAt == nil {
		current.job.StartedAt = &now
	}
	current.job.UpdatedAt = now
	job := current.job
	if err := s.store.Update(context.Background(), job); err != nil {
		s.log.Error("provisioning phase could not be persisted", "module", "Provisioning.Phase", "job_id", job.ID, "phase", status, "error", err)
	}
	s.store.Event(context.Background(), job.ID, status, "PHASE_CHANGED", summary, now)
	s.mu.Unlock()
	s.log.Info("provisioning phase changed", "module", "Provisioning.Phase", "job_id", job.ID, "template_id", job.TemplateID, "phase", status, "summary", summary)
}
func (s *Service) installationCompleted(current *run) {
	s.mu.Lock()
	current.job.InstallationCompleted = true
	s.mu.Unlock()
}
func (s *Service) fail(current *run, phase, code, summary, errorSummary string, files bool) {
	s.mu.Lock()
	current.job.FailurePhase = phase
	current.job.FailureCode = code
	current.job.RegistrationRecoverable = current.job.InstallationCompleted && phase == RegisteringServer
	s.mu.Unlock()
	s.finish(current, Failed, summary, errorSummary, files, "")
}
func (s *Service) finish(current *run, status, summary, errorSummary string, files bool, serverID string) {
	current.once.Do(func() {
		s.mu.Lock()
		now := s.now()
		current.job.Status = status
		current.job.CurrentPhase = status
		current.job.Summary = summary
		current.job.ErrorSummary = errorSummary
		current.job.FilesMayRemain = files
		current.job.ServerID = serverID
		current.job.CompletedAt = &now
		current.job.UpdatedAt = now
		job := current.job
		observer := s.observer
		s.mu.Unlock()
		if err := s.store.Update(context.Background(), job); err != nil {
			s.log.Error("terminal provisioning state could not be persisted", "module", "Provisioning.Complete", "job_id", job.ID, "status", status, "error", err)
		}
		code := "JOB_COMPLETED"
		if status == Failed {
			code = "JOB_FAILED"
		}
		if status == Cancelled {
			code = "JOB_CANCELLED"
		}
		s.store.Event(context.Background(), job.ID, job.FailurePhase, code, summary, now)
		if observer != nil {
			action := map[string]string{Completed: "server.provision_complete", Failed: "server.provision_fail", Cancelled: "server.provision_cancel"}[status]
			observer(Event{action, job, now.Sub(job.CreatedAt)})
		}
		if status == Failed {
			s.log.Error("provisioning job failed", "module", "Provisioning.Complete", "job_id", job.ID, "template_id", job.TemplateID, "phase", job.FailurePhase, "failure_code", job.FailureCode, "summary", errorSummary)
		} else {
			s.log.Info("provisioning job finished", "module", "Provisioning.Complete", "job_id", job.ID, "template_id", job.TemplateID, "status", status, "server_id", serverID)
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

// validateInstallation is deliberately separate from server registration: a
// successful SteamCMD process only becomes an installation success after the
// template-owned launch artifact is present under the managed root.
func validateInstallation(launch templates.LaunchDefinition, root string, values map[string]string) error {
	known := make(map[string]bool, len(values))
	for key := range values {
		known[key] = true
	}
	executable, err := templates.ExpandRelativePath(launch.Executable, values, known)
	if err != nil {
		return err
	}
	path := filepath.Join(root, filepath.FromSlash(executable))
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !inside(root, resolved) {
		return errors.New("expected executable is missing or unsafe")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("expected executable is missing or unsafe")
	}
	return nil
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
func recoverableTarget(ctx context.Context, store *Store, root, templateID, directory, actorID string) error {
	data, err := os.ReadFile(filepath.Join(root, ".gamenode-provisioning.json"))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return ErrRecoveryUnavailable
	}
	var marker struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal(data, &marker) != nil || marker.JobID == "" {
		return ErrRecoveryUnavailable
	}
	job, err := store.Get(ctx, marker.JobID)
	if err != nil || job.Status != Failed || !job.FilesMayRemain || job.TemplateID != templateID || job.DirectoryName != directory || job.ActorUserID != actorID {
		return ErrRecoveryUnavailable
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

type outputWriter struct {
	service   *Service
	jobID     string
	values    map[string]string
	sensitive map[string]bool
}

func (w outputWriter) Write(value []byte) (int, error) {
	w.service.appendOutput(w.jobID, string(value), w.values, w.sensitive)
	return len(value), nil
}

func (s *Service) outputWriter(jobID string, values map[string]string, sensitive map[string]bool) io.Writer {
	return outputWriter{service: s, jobID: jobID, values: values, sensitive: sensitive}
}

func (s *Service) appendOutput(jobID, value string, values map[string]string, sensitive map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := s.outputs[jobID]
	if output == nil || output.truncated {
		return
	}
	for key, isSensitive := range sensitive {
		if isSensitive && values[key] != "" {
			value = strings.ReplaceAll(value, values[key], "[REDACTED]")
		}
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	for len(value) > 0 {
		line, rest, found := strings.Cut(value, "\n")
		if found {
			s.appendOutputLine(output, output.pending+line)
			output.pending = ""
			value = rest
			continue
		}
		output.pending += line
		if len(output.pending) > 16<<10 {
			s.appendOutputLine(output, output.pending[:16<<10])
			output.pending = ""
		}
		break
	}
}

func (s *Service) installerReportedDiskSpace(jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := s.outputs[jobID]
	if output == nil {
		return false
	}
	text := strings.ToLower(strings.Join(append(append([]string(nil), output.lines...), output.pending), "\n"))
	return strings.Contains(text, "not enough disk space") || strings.Contains(text, "failed to preallocate")
}

func (s *Service) appendOutputLine(output *installerOutput, line string) {
	if output.truncated {
		return
	}
	if len(output.lines) >= maxInstallerOutputLines || output.bytes+len(line) > maxInstallerOutputBytes {
		output.truncated = true
		return
	}
	output.lines = append(output.lines, line)
	output.bytes += len(line)
}

func (s *Service) pruneOutputsLocked() {
	for len(s.outputOrder) > maxInstallerOutputJobs {
		id := s.outputOrder[0]
		s.outputOrder = s.outputOrder[1:]
		delete(s.outputs, id)
	}
}
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
