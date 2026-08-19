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

	"gamenode/internal/filesystem"
	"gamenode/internal/gameconfig"
	"gamenode/internal/ports"
	gameruntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
	"gamenode/internal/tenants"
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
	RuntimeNative           = "native"
	RuntimeContainer        = "container"
	DefaultContainerMemory  = int64(1 << 30)
	DefaultContainerCPU     = 1000
	DefaultContainerPIDs    = int64(512)
	DefaultContainerTmpfs   = int64(256 << 20)
)

var (
	ErrNotProvisionable    = errors.New("template is not provisionable on this host")
	ErrTargetConflict      = errors.New("server target is already populated or reserved")
	ErrRecoveryUnavailable = errors.New("installed server cannot be recovered")
	ErrJobNotActive        = errors.New("provisioning job is not active")
	// ErrPortPreflightFailed is returned when the ports a template resolves
	// to are already known to conflict with another GameNode server or the
	// host, before SteamCMD installation starts. It wraps the underlying
	// internal/ports collision error, which is safe to surface: it names
	// only the conflicting protocol/port, never host internals. This is a
	// fail-fast usability check, not a reservation - the final server
	// registration still runs the same check authoritatively, because a
	// port can become occupied between preflight and registration.
	ErrPortPreflightFailed = errors.New("requested ports are not available")
	// ErrNamePreflightFailed is returned when the requested server name is
	// already known to be taken by another GameNode server, before SteamCMD
	// installation starts. It wraps internal/servers' ErrDuplicateName,
	// which is safe to surface. Like ErrPortPreflightFailed, this is a
	// fail-fast usability check, not a reservation - the final server
	// registration still runs the same check authoritatively.
	ErrNamePreflightFailed = errors.New("requested server name is not available")
	// ErrInvalidTenant is returned when a provisioning request references a
	// tenant ID that does not exist. It is never inferred from a raw host
	// path or from client-supplied tenant name/slug text.
	ErrInvalidTenant               = errors.New("invalid tenant")
	ErrContainerRuntimeUnavailable = errors.New("container Egg runtime is unavailable")
	ErrContainerImagePolicy        = errors.New("container image is blocked by policy")
	ErrContainerImageSelection     = errors.New("selected container image is not declared by the Egg")
	directoryPattern               = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type Job struct {
	ID                      string     `json:"id"`
	TenantID                string     `json:"tenant_id"`
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
	RuntimeType             string     `json:"runtime_type"`
	SelectedImage           string     `json:"selected_image,omitempty"`
	SelectedImageDigest     string     `json:"selected_image_digest,omitempty"`
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
	// TenantID is the tenant the new managed server belongs to. Left empty,
	// Start defaults it to tenants.DefaultTenantID, matching
	// servers.Server.Validate's identical default so this technical
	// interface is ready without a caller having to select a tenant yet; see
	// docs/architecture.md's Tenant Foundation Step 2 section. It is never
	// taken from a raw host path, so a normal user reaching this request
	// cannot inject an arbitrary filesystem location through it.
	TenantID         string
	RuntimeType      string
	Image            string
	MemoryLimitBytes int64
	CPULimitMillis   int
	PIDsLimit        int64
	TmpfsSizeBytes   int64
}
type Event struct {
	Action   string
	Job      Job
	Duration time.Duration
}
type Provisionability struct {
	Provisionable          bool                    `json:"provisionable"`
	HostPlatform           string                  `json:"host_platform"`
	Code                   string                  `json:"code,omitempty"`
	Summary                string                  `json:"summary"`
	Installer              string                  `json:"installer,omitempty"`
	AppID                  int                     `json:"app_id,omitempty"`
	Validate               bool                    `json:"validate"`
	LaunchExecutable       string                  `json:"launch_executable,omitempty"`
	NativeCompatibility    templates.Compatibility `json:"native_compatibility"`
	ContainerCompatibility templates.Compatibility `json:"container_compatibility"`
	ContainerImages        []string                `json:"container_images,omitempty"`
	ContainerImagePolicy   []string                `json:"container_image_policy,omitempty"`
}
type Observer func(Event)

type TemplateSource interface {
	Get(context.Context, string) (templates.Template, error)
}
type Installer interface {
	Install(context.Context, string, steamcmd.InstallPlan, io.Writer, steamcmd.EventSink) error
}
type ContainerInstaller interface {
	Available(context.Context) error
	PullImage(context.Context, string) error
	RunInstaller(context.Context, gameruntime.ContainerInstallSpec, io.Writer) error
}
type containerImageDigestResolver interface {
	ImageDigest(context.Context, string) (string, error)
}
type ServerCreator interface {
	CreateProvisioned(context.Context, servers.Server, string, []servers.ProvisionedVariable, []ports.Port, []servers.ProvisionedConfigAdapter, *servers.ProvisionedSteamCMD) (servers.Record, error)
	// NameAvailable backs Start's early name preflight; see
	// ErrNamePreflightFailed.
	NameAvailable(context.Context, string) error
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db} }
func (s *Store) Create(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO provisioning_jobs(id,tenant_id,actor_user_id,actor_username,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,error_summary,files_may_remain,server_id,created_at,started_at,completed_at,updated_at,current_phase,last_successful_phase,failure_phase,failure_code,installation_completed,registration_recoverable,runtime_type,selected_image,selected_image_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.TenantID, j.ActorUserID, j.ActorUsername, j.TemplateID, j.TemplateName, j.ServerName, j.DirectoryName, j.InstallerType, j.AppID, j.Status, j.Summary, j.ErrorSummary, j.FilesMayRemain, nil, stamp(j.CreatedAt), nil, nil, stamp(j.UpdatedAt), j.CurrentPhase, j.LastSuccessfulPhase, j.FailurePhase, j.FailureCode, j.InstallationCompleted, j.RegistrationRecoverable, defaultRuntime(j.RuntimeType), j.SelectedImage, j.SelectedImageDigest)
	return err
}
func (s *Store) Update(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `UPDATE provisioning_jobs SET status=?,summary=?,error_summary=?,files_may_remain=?,server_id=?,started_at=?,completed_at=?,updated_at=?,current_phase=?,last_successful_phase=?,failure_phase=?,failure_code=?,installation_completed=?,registration_recoverable=?,runtime_type=?,selected_image=?,selected_image_digest=? WHERE id=?`, j.Status, j.Summary, j.ErrorSummary, j.FilesMayRemain, nullable(j.ServerID), nullableTime(j.StartedAt), nullableTime(j.CompletedAt), stamp(j.UpdatedAt), j.CurrentPhase, j.LastSuccessfulPhase, j.FailurePhase, j.FailureCode, j.InstallationCompleted, j.RegistrationRecoverable, defaultRuntime(j.RuntimeType), j.SelectedImage, j.SelectedImageDigest, j.ID)
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
	err := s.db.QueryRowContext(ctx, `SELECT id,tenant_id,actor_user_id,actor_username,template_id,template_name,server_name,directory_name,installer_type,app_id,status,summary,error_summary,files_may_remain,server_id,created_at,started_at,completed_at,updated_at,current_phase,last_successful_phase,failure_phase,failure_code,installation_completed,registration_recoverable,runtime_type,selected_image,selected_image_digest FROM provisioning_jobs WHERE id=?`, id).Scan(&j.ID, &j.TenantID, &j.ActorUserID, &j.ActorUsername, &j.TemplateID, &j.TemplateName, &j.ServerName, &j.DirectoryName, &j.InstallerType, &j.AppID, &j.Status, &j.Summary, &j.ErrorSummary, &remains, &server, &created, &started, &completed, &updated, &j.CurrentPhase, &j.LastSuccessfulPhase, &j.FailurePhase, &j.FailureCode, &j.InstallationCompleted, &j.RegistrationRecoverable, &j.RuntimeType, &j.SelectedImage, &j.SelectedImageDigest)
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
	_, err := s.db.ExecContext(ctx, `UPDATE provisioning_jobs SET status='failed',summary='Provisioning was interrupted by a GameNode restart',error_summary='GameNode restarted during provisioning; target files may remain',files_may_remain=1,failure_phase=current_phase,failure_code='INTERRUPTED',registration_recoverable=CASE WHEN installation_completed=1 AND registration_snapshot_json<>'' THEN 1 ELSE 0 END,current_phase='failed',completed_at=?,updated_at=? WHERE status IN ('pending','preparing','downloading_steamcmd','steamcmd_ready','installing','steamcmd_completed','validating_installation','installation_validated','resolving_launch','registering_server','server_registered','creating_server')`, now, now)
	return err
}

func (s *Store) ServerExists(ctx context.Context, id string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE id=?`, id).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

// TenantExists reads the tenants table directly rather than importing
// internal/tenants' Service, matching the existing cross-domain read
// convention (see identity.ListGroupSummaries and servers.Store.requireTenant).
func (s *Store) TenantExists(ctx context.Context, id string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenants WHERE id=?`, id).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

type Service struct {
	store              *Store
	templates          TemplateSource
	installer          Installer
	containerInstaller ContainerInstaller
	imagePolicy        ImagePolicy
	containerTimeout   time.Duration
	servers            ServerCreator
	// ports reuses internal/ports' authoritative collision and best-effort
	// availability logic for the early preflight in Start. Provisioning
	// never duplicates that policy.
	ports *ports.Service
	// dataRoot is the GameNode data directory. Managed/provisioned server
	// storage is always resolved from it through tenants.TenantServerRoot;
	// this field alone is never joined with a caller-controlled path
	// fragment.
	dataRoot    string
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
	// managedSecrets records that this registration carries managed secret
	// values. They are deliberately never written to job state, so a retry
	// could only create a server with silently missing configuration. Such a
	// registration is therefore not recoverable.
	managedSecrets bool
}
type registrationSnapshot struct {
	Server         servers.Server                     `json:"server"`
	TemplateID     string                             `json:"template_id"`
	Variables      []servers.ProvisionedVariable      `json:"variables"`
	Ports          []ports.Port                       `json:"ports"`
	ConfigAdapters []servers.ProvisionedConfigAdapter `json:"config_adapters"`
	SteamCMD       servers.ProvisionedSteamCMD        `json:"steamcmd"`
}
type installerOutput struct {
	lines     []string
	pending   string
	bytes     int
	truncated bool
}
type Options struct {
	HostOS             string
	Log                *slog.Logger
	ContainerInstaller ContainerInstaller
	ImagePolicy        ImagePolicy
	ContainerTimeout   time.Duration
}

// ImagePolicy is an allow-only registry policy. Registry-less Docker image
// references are treated as Docker Hub (docker.io); no credentials or remote
// endpoint are accepted here.
type ImagePolicy struct {
	AllowedRegistries []string
}

func DefaultImagePolicy() ImagePolicy {
	return ImagePolicy{AllowedRegistries: []string{"docker.io", "ghcr.io", "quay.io"}}
}

func (p ImagePolicy) normalized() []string {
	values := p.AllowedRegistries
	if len(values) == 0 {
		values = DefaultImagePolicy().AllowedRegistries
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] && len(value) <= 128 && !strings.ContainsAny(value, "/@ \t\r\n") {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (p ImagePolicy) RegistryAllowed(image string) bool {
	image = strings.ToLower(strings.TrimSpace(image))
	parts := strings.SplitN(strings.SplitN(image, "@", 2)[0], "/", 2)
	registry := "docker.io"
	if len(parts) == 2 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		registry = parts[0]
	}
	for _, allowed := range p.normalized() {
		if registry == allowed {
			return true
		}
	}
	return false
}

func (p ImagePolicy) Validate(images ...string) error {
	for _, image := range images {
		if err := templates.ValidateContainerImageReference(image); err != nil {
			return ErrContainerImageSelection
		}
		if !p.RegistryAllowed(image) {
			return fmt.Errorf("%w: image registry is not allowed", ErrContainerImagePolicy)
		}
	}
	return nil
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
	policy := options.ImagePolicy
	if len(policy.AllowedRegistries) == 0 {
		policy = DefaultImagePolicy()
	}
	timeout := options.ContainerTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	if timeout < time.Second || timeout > 2*time.Hour {
		timeout = 30 * time.Minute
	}
	return &Service{store: NewStore(db), templates: source, installer: installer, containerInstaller: options.ContainerInstaller, imagePolicy: policy, containerTimeout: timeout, servers: creator, ports: ports.New(db), dataRoot: filepath.Clean(dataDirectory), hostOS: host, ctx: ctx, cancel: cancel, active: map[string]*run{}, roots: map[string]string{}, outputs: map[string]*installerOutput{}, now: func() time.Time { return time.Now().UTC() }, log: log}
}

func (s *Service) SetContainerInstaller(installer ContainerInstaller) {
	s.mu.Lock()
	s.containerInstaller = installer
	s.mu.Unlock()
}

func (s *Service) SetImagePolicy(policy ImagePolicy) {
	s.mu.Lock()
	if len(policy.AllowedRegistries) == 0 {
		policy = DefaultImagePolicy()
	}
	s.imagePolicy = policy
	s.mu.Unlock()
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
		if current.cancel != nil {
			current.cancel()
		}
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
	runtimeType := strings.TrimSpace(request.RuntimeType)
	if runtimeType == "" {
		runtimeType = RuntimeNative
	}
	var plan steamcmd.InstallPlan
	var containerPlan *templates.ContainerEggRuntimePlan
	selectedImage := ""
	if runtimeType == RuntimeContainer {
		containerPlan = template.ContainerRuntime
		if containerPlan == nil || template.ContainerCompatibility.Status == templates.Unsupported {
			return Job{}, fmt.Errorf("%w: container compatibility is unsupported", ErrNotProvisionable)
		}
		selectedImage = strings.TrimSpace(request.Image)
		if selectedImage == "" && len(containerPlan.Images) > 0 {
			selectedImage = containerPlan.Images[0]
		}
		if !containsImage(containerPlan.Images, selectedImage) {
			return Job{}, ErrContainerImageSelection
		}
		s.mu.Lock()
		policy := s.imagePolicy
		containerInstaller := s.containerInstaller
		s.mu.Unlock()
		if err = policy.Validate(selectedImage, containerPlan.InstallerImage); err != nil {
			return Job{}, err
		}
		if containerInstaller == nil {
			return Job{}, ErrContainerRuntimeUnavailable
		}
	} else if runtimeType == RuntimeNative {
		plan, err = CheckProvisionable(template, values, s.hostOS)
		if err != nil {
			return Job{}, err
		}
		if _, ok := templates.LaunchForPlatform(template, s.hostOS); !ok {
			return Job{}, ErrNotProvisionable
		}
	} else {
		return Job{}, errors.New("runtime type must be native or container")
	}
	provisionedPorts, err := resolveTemplatePorts(template.Ports, values, runtimeType == RuntimeContainer)
	if err != nil {
		return Job{}, err
	}
	// Fail fast before SteamCMD/game installation or any managed-server
	// target is reserved: a port conflict is already knowable from the
	// resolved template ports, so there is no reason to download the game
	// first. This reuses internal/ports' authoritative collision and
	// best-effort OS availability logic; it is a usability check only. The
	// final server registration below runs the same check again inside its
	// transaction, which stays authoritative because ports are never
	// reserved between preflight and registration.
	if len(provisionedPorts) > 0 {
		if err = s.ports.CheckCandidates(ctx, provisionedPorts); err != nil {
			return Job{}, fmt.Errorf("%w: %w", ErrPortPreflightFailed, err)
		}
	}
	serverName := strings.TrimSpace(request.ServerName)
	if serverName == "" || len(serverName) > 100 {
		return Job{}, errors.New("server name must be 1 to 100 characters")
	}
	// Same fail-fast reasoning as the port preflight above: servers.name is
	// COLLATE NOCASE UNIQUE, so a duplicate name is already knowable before
	// SteamCMD runs. This reuses internal/servers' authoritative check; it is
	// a usability check only, not a reservation - the final server
	// registration below (inside servers.Store.CreateProvisioned's
	// transaction) re-runs the same check and stays authoritative for the
	// unavoidable TOCTOU window between preflight and registration.
	if err = s.servers.NameAvailable(ctx, serverName); err != nil {
		return Job{}, fmt.Errorf("%w: %w", ErrNamePreflightFailed, err)
	}
	tenantID := strings.TrimSpace(request.TenantID)
	if tenantID == "" {
		// Matching servers.Server.Validate's identical default, this keeps
		// every existing caller working without a tenant selection surface;
		// see the Request.TenantID doc comment.
		tenantID = tenants.DefaultTenantID
	}
	tenantExists, err := s.store.TenantExists(ctx, tenantID)
	if err != nil {
		return Job{}, err
	}
	if !tenantExists {
		return Job{}, ErrInvalidTenant
	}
	root, err := tenants.TenantServerRoot(s.dataRoot, tenantID, request.DirectoryName)
	if err != nil {
		return Job{}, err
	}
	if request.RecoverExisting {
		if err = recoverableTarget(ctx, s.store, root, template.ID, request.DirectoryName, tenantID, request.ActorUserID); err != nil {
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
	installerType := template.Installer.Type
	if runtimeType == RuntimeContainer {
		installerType = RuntimeContainer
	}
	job := Job{ID: id, TenantID: tenantID, TemplateID: template.ID, TemplateName: template.Name, ServerName: serverName, DirectoryName: request.DirectoryName, InstallerType: installerType, AppID: plan.AppID, RuntimeType: runtimeType, SelectedImage: selectedImage, Status: Pending, CurrentPhase: Pending, Summary: "Provisioning is queued", CreatedAt: now, UpdatedAt: now, ActorUserID: request.ActorUserID, ActorUsername: request.ActorUsername}
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
	go s.execute(jobCtx, current, template, values, sensitive, provisionedPorts, plan, runtimeType, containerPlan, selectedImage, request)
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

func containsImage(images []string, selected string) bool {
	for _, image := range images {
		if image == selected {
			return true
		}
	}
	return false
}

func defaultRuntime(value string) string {
	if value == "" {
		return RuntimeNative
	}
	return value
}

func containerResources(plan *templates.ContainerEggRuntimePlan, request Request) (int64, int, int64, int64, error) {
	memory, cpu, pids, tmpfs := DefaultContainerMemory, DefaultContainerCPU, DefaultContainerPIDs, DefaultContainerTmpfs
	if plan != nil {
		if plan.ResourceDefaults.MemoryLimitBytes > 0 {
			memory = plan.ResourceDefaults.MemoryLimitBytes
		}
		if plan.ResourceDefaults.CPULimitMillis > 0 {
			cpu = plan.ResourceDefaults.CPULimitMillis
		}
		if plan.ResourceDefaults.PIDsLimit > 0 {
			pids = plan.ResourceDefaults.PIDsLimit
		}
		if plan.ResourceDefaults.TempSizeBytes > 0 {
			tmpfs = plan.ResourceDefaults.TempSizeBytes
		}
	}
	if request.MemoryLimitBytes != 0 {
		memory = request.MemoryLimitBytes
	}
	if request.CPULimitMillis != 0 {
		cpu = request.CPULimitMillis
	}
	if request.PIDsLimit != 0 {
		pids = request.PIDsLimit
	}
	if request.TmpfsSizeBytes != 0 {
		tmpfs = request.TmpfsSizeBytes
	}
	if memory < 16<<20 || memory > 1<<50 || cpu < 10 || cpu > 1_000_000 || pids < 1 || pids > 32768 || tmpfs < 1<<20 || tmpfs > 1<<30 {
		return 0, 0, 0, 0, errors.New("container resource limits are invalid")
	}
	return memory, cpu, pids, tmpfs, nil
}

func (s *Service) executeContainer(ctx context.Context, current *run, template templates.Template, values map[string]string, sensitive map[string]bool, provisionedPorts []ports.Port, plan *templates.ContainerEggRuntimePlan, selectedImage string, request Request, created, recovering bool) {
	if plan == nil {
		s.finish(current, Failed, "Container Egg provisioning failed", "Container runtime plan is unavailable", created, "")
		return
	}
	memory, cpu, pids, tmpfs, err := containerResources(plan, request)
	if err != nil {
		s.fail(current, Preparing, "CONTAINER_RESOURCES_INVALID", "Container resource validation failed", "Container resource limits are outside the supported policy", created)
		return
	}
	selectedImageDigest := ""
	if !recovering {
		s.mu.Lock()
		installer := s.containerInstaller
		timeout := s.containerTimeout
		s.mu.Unlock()
		if installer == nil {
			s.fail(current, Preparing, "CONTAINER_ENGINE_UNAVAILABLE", "Container runtime unavailable", "The container engine is not available for Egg installation", created)
			return
		}
		s.phase(current, Installing, "Checking the installer image policy")
		if err = installer.Available(ctx); err != nil {
			s.fail(current, Installing, "CONTAINER_ENGINE_UNAVAILABLE", "Container runtime unavailable", "The container engine is not available for Egg installation", created)
			return
		}
		if err = installer.PullImage(ctx, selectedImage); err != nil {
			if ctx.Err() != nil {
				s.finish(current, Cancelled, "Provisioning was cancelled", "", created, "")
			} else {
				s.fail(current, Installing, "CONTAINER_IMAGE_PULL_FAILED", "Game image pull failed", "GameNode could not pull the approved selected image", created)
			}
			return
		}
		if err = installer.PullImage(ctx, plan.InstallerImage); err != nil {
			if ctx.Err() != nil {
				s.finish(current, Cancelled, "Provisioning was cancelled", "", created, "")
			} else {
				s.fail(current, Installing, "CONTAINER_INSTALL_IMAGE_PULL_FAILED", "Installer image pull failed", "GameNode could not pull the approved installer image", created)
			}
			return
		}
		if resolver, ok := installer.(containerImageDigestResolver); ok {
			// Digest lookup is best effort because some compatible Engine API
			// implementations expose only tag availability. When available it
			// is persisted as the immutable provisioning snapshot.
			selectedImageDigest, _ = resolver.ImageDigest(ctx, selectedImage)
		}
		if selectedImageDigest != "" {
			current.job.SelectedImageDigest = selectedImageDigest
			_ = s.store.Update(context.Background(), current.job)
		}
		s.phase(current, SteamCMDReady, "Installer image ready")
		environment := make(map[string]string, len(values)+1)
		for key, value := range values {
			environment[key] = value
		}
		environment["SERVER_ROOT"] = "/home/container"
		installContext, cancel := context.WithTimeout(ctx, timeout)
		err = installer.RunInstaller(installContext, gameruntime.ContainerInstallSpec{Image: plan.InstallerImage, Entrypoint: plan.InstallerEntrypoint, Script: plan.InstallationScript, WorkingDirectory: current.root, Environment: environment, MemoryLimitBytes: memory, CPULimitMillis: cpu, PIDsLimit: pids, TmpfsSizeBytes: tmpfs, ServerID: current.job.ID, Generation: current.job.ID, OwnershipToken: current.job.ID}, s.outputWriter(current.job.ID, values, sensitive))
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				s.finish(current, Cancelled, "Provisioning was cancelled", "", true, "")
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				s.fail(current, Installing, "CONTAINER_INSTALL_TIMEOUT", "Installer timed out", "The Egg installer exceeded the bounded execution timeout; files may remain", created)
				return
			}
			s.fail(current, Installing, "CONTAINER_INSTALL_FAILED", "Installer failed", "The Egg installer exited unsuccessfully; files may remain", created)
			return
		}
		s.phase(current, SteamCMDCompleted, "Installer container completed")
		s.installationCompleted(current)
	}
	if len(plan.ConfigOperations) > 0 {
		s.phase(current, ResolvingLaunch, "Applying validated container configuration")
		known := make(map[string]bool, len(template.Variables))
		for _, variable := range template.Variables {
			known[variable.Key] = true
		}
		if err = applyContainerConfigOperations(current.root, plan.ConfigOperations, values, known); err != nil {
			s.fail(current, ResolvingLaunch, "CONTAINER_CONFIG_FAILED", "Container configuration failed", "A declared container configuration operation could not be applied safely", true)
			return
		}
	}
	if !recovering {
		s.phase(current, ValidatingInstallation, "Validating the persistent server root")
	}
	if err = validateContainerInstallation(current.root); err != nil {
		s.fail(current, ValidatingInstallation, "CONTAINER_INSTALLATION_INVALID", "Installation validation failed", "The installer did not leave a valid persistent server root", true)
		return
	}
	s.phase(current, InstallationValidated, "Persistent server root validated")
	s.phase(current, ResolvingLaunch, "Resolving container startup")
	server, metadata, err := buildContainerServer(template, plan, selectedImage, selectedImageDigest, current.job.ServerName, current.job.TenantID, current.root, values, sensitive, provisionedPorts, memory, cpu, pids, tmpfs)
	if err != nil {
		s.fail(current, ResolvingLaunch, "CONTAINER_STARTUP_INVALID", "Container startup resolution failed", "GameNode could not create a safe container startup snapshot", true)
		return
	}
	managedSecrets := false
	for _, value := range sensitive {
		if value {
			managedSecrets = true
			break
		}
	}
	if !managedSecrets {
		snapshot, marshalErr := json.Marshal(registrationSnapshot{Server: server, TemplateID: template.ID, Variables: metadata, Ports: provisionedPorts})
		if marshalErr != nil || s.store.SaveRegistrationSnapshot(context.Background(), current.job.ID, snapshot) != nil {
			s.fail(current, RegisteringServer, "SERVER_RELATED_DATA_FAILED", "Server registration failed", "Game files were installed successfully, but GameNode could not prepare the server registration", true)
			return
		}
	} else {
		current.managedSecrets = true
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
	record, err := s.servers.CreateProvisioned(ctx, server, template.ID, metadata, provisionedPorts, nil, nil)
	if err != nil {
		s.fail(current, RegisteringServer, "SERVER_DB_CREATE_FAILED", "Server registration failed", "Game files were installed successfully, but GameNode could not save the server definition", true)
		return
	}
	s.phase(current, ServerRegistered, "Server registered successfully")
	_ = os.Remove(filepath.Join(current.root, ".gamenode-provisioning.json"))
	s.finish(current, Completed, "Server installed successfully", "", false, record.Server.ID)
}

func applyContainerConfigOperations(root string, operations []templates.ContainerConfigOperation, values map[string]string, known map[string]bool) error {
	files := filesystem.New()
	for _, operation := range operations {
		target, err := templates.ExpandRelativePath(operation.Target, values, known)
		if err != nil {
			return err
		}
		replacement, err := templates.Expand(operation.Property, values, known)
		if err != nil {
			return err
		}
		content, err := files.ReadFile(root, target)
		if err != nil {
			if errors.Is(err, filesystem.ErrNotFound) && !operation.Required {
				continue
			}
			return err
		}
		updated := content.Content
		changed := false
		switch operation.Format {
		case "properties", "key-value", "ini":
			updated, changed, err = replaceContainerKeyValue(updated, operation.Key, replacement)
		case "json":
			updated, changed, err = replaceContainerJSON(updated, operation.Key, replacement)
		default:
			err = errors.New("unsupported container configuration format")
		}
		if err != nil {
			return err
		}
		if !changed {
			if operation.Required {
				return errors.New("required container configuration key is missing or ambiguous")
			}
			continue
		}
		if err = files.WriteFile(root, target, updated); err != nil {
			return err
		}
	}
	return nil
}

func replaceContainerKeyValue(content, key, replacement string) (string, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "=\r\n") {
		return content, false, errors.New("invalid container configuration key")
	}
	lines := strings.SplitAfter(content, "\n")
	match := -1
	for index, line := range lines {
		body := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		trimmed := strings.TrimSpace(body)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		separator := strings.IndexByte(body, '=')
		if separator < 0 || strings.TrimSpace(body[:separator]) != key {
			continue
		}
		if match >= 0 {
			return content, false, errors.New("container configuration key is ambiguous")
		}
		match = index
	}
	if match < 0 {
		return content, false, nil
	}
	line := lines[match]
	separator := strings.IndexByte(line, '=')
	ending := ""
	if strings.HasSuffix(line, "\r\n") {
		ending = "\r\n"
	} else if strings.HasSuffix(line, "\n") {
		ending = "\n"
	} else if strings.HasSuffix(line, "\r") {
		ending = "\r"
	}
	lines[match] = line[:separator+1] + replacement + ending
	return strings.Join(lines, ""), true, nil
}

func replaceContainerJSON(content, key, replacement string) (string, bool, error) {
	var document map[string]any
	if err := json.Unmarshal([]byte(content), &document); err != nil {
		return content, false, err
	}
	parts := strings.Split(strings.TrimSpace(key), ".")
	if len(parts) == 0 || parts[0] == "" {
		return content, false, errors.New("invalid container JSON key")
	}
	current := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			return content, false, nil
		}
		current = next
	}
	leaf := parts[len(parts)-1]
	if _, ok := current[leaf]; !ok {
		return content, false, nil
	}
	current[leaf] = replacement
	updated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return content, false, err
	}
	if strings.HasSuffix(content, "\n") {
		updated = append(updated, '\n')
	}
	return string(updated), true, nil
}

func validateContainerInstallation(root string) error {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return errors.New("persistent server root is unavailable")
	}
	return nil
}

func buildContainerServer(template templates.Template, plan *templates.ContainerEggRuntimePlan, image, imageDigest, name, tenantID, root string, values map[string]string, sensitive map[string]bool, provisionedPorts []ports.Port, memory int64, cpu int, pids, tmpfs int64) (servers.Server, []servers.ProvisionedVariable, error) {
	serverID, err := newServerID()
	if err != nil {
		return servers.Server{}, nil, err
	}
	environment := make(map[string]string, len(values))
	metadata := make([]servers.ProvisionedVariable, 0, len(values))
	sensitivity := make(map[string]bool, len(sensitive))
	for key, value := range values {
		environment[key] = value
		metadata = append(metadata, servers.ProvisionedVariable{Key: key, Sensitive: sensitive[key], Source: template.SourceType, Version: template.Version})
		sensitivity[key] = sensitive[key]
	}
	snapshot := &servers.EggRuntimeSnapshot{SourceType: template.SourceType, SourceIdentifier: template.SourceIdentifier, SourceHash: template.SourceMetadata.OriginalHash, SourceFormatVersion: template.SourceFormatVersion, TemplateVersion: template.Version, SelectedImage: image, ImageDigest: imageDigest, StartupTemplate: plan.StartupTemplate, StartupShell: plan.StartupShell, VariableSensitivity: sensitivity, ResourceDefaults: servers.ContainerResourceSnapshot{MemoryLimitBytes: memory, CPULimitMillis: cpu, PIDsLimit: pids, TmpfsSizeBytes: tmpfs}}
	for _, port := range provisionedPorts {
		snapshot.Ports = append(snapshot.Ports, servers.ContainerPortSnapshot{Name: port.Name, Protocol: port.Protocol, HostPort: port.Port, ContainerPort: port.ContainerPort})
	}
	for _, operation := range plan.ConfigOperations {
		snapshot.ConfigOperations = append(snapshot.ConfigOperations, servers.ContainerConfigSnapshotOperation{Format: operation.Format, Target: operation.Target, Key: operation.Key, Property: operation.Property, Required: operation.Required})
	}
	config := &servers.ContainerConfig{Image: image, ImageDigest: imageDigest, Command: []string{plan.StartupShell, "-lc", plan.StartupTemplate}, MemoryLimitBytes: memory, CPULimitMillis: cpu, PIDsLimit: pids, TmpfsSizeBytes: tmpfs, StartupShell: plan.StartupShell, StartupTemplate: plan.StartupTemplate, EggSnapshot: snapshot}
	server := servers.Server{ID: serverID, TenantID: tenantID, CreationMode: servers.CreationTemplate, Name: name, Description: template.Description, WorkingDirectory: root, RuntimeType: servers.RuntimeContainer, EnvironmentVariables: environment, Container: config, RestartPolicy: "never", StopMethod: "terminate", StopTimeoutSeconds: 30, AutoRestartMaxAttempts: 3, AutoRestartWindowSeconds: 300, AutoRestartDelaySeconds: 5}
	if err = server.Validate(); err != nil {
		return servers.Server{}, nil, err
	}
	return server, metadata, nil
}
func (s *Service) Check(ctx context.Context, templateID string) (Provisionability, error) {
	template, err := s.templates.Get(ctx, templateID)
	if err != nil {
		return Provisionability{}, err
	}
	result := Provisionability{HostPlatform: s.hostOS, NativeCompatibility: template.NativeCompatibility, ContainerCompatibility: template.ContainerCompatibility}
	if result.NativeCompatibility.Status == "" {
		result.NativeCompatibility = template.Compatibility
	}
	if result.ContainerCompatibility.Status == "" && template.ContainerRuntime != nil {
		result.ContainerCompatibility = templates.Compatibility{Status: templates.Compatible}
	}
	if template.ContainerRuntime != nil {
		result.ContainerImages = append([]string(nil), template.ContainerRuntime.Images...)
		s.mu.Lock()
		policy := s.imagePolicy
		s.mu.Unlock()
		result.ContainerImagePolicy = policy.normalized()
		allowed := false
		for _, image := range template.ContainerRuntime.Images {
			if policy.Validate(image) == nil {
				allowed = true
				break
			}
		}
		if !allowed {
			result.ContainerCompatibility.Status = "blocked_by_image_policy"
			result.Code = "CONTAINER_IMAGE_POLICY_BLOCKED"
			result.Summary = "The Egg has no image allowed by the node image policy"
			result.Provisionable = false
		}
	}
	values := make(map[string]string, len(template.Variables))
	for _, variable := range template.Variables {
		values[variable.Key] = variable.DefaultValue
	}
	plan, err := CheckProvisionable(template, values, s.hostOS)
	if err != nil {
		code, summary := provisionabilityFailure(err)
		result.Provisionable = result.ContainerCompatibility.Status == templates.Compatible || result.ContainerCompatibility.Status == templates.PartiallyCompatible
		if result.Code == "" {
			result.Code, result.Summary = code, summary
		}
	} else {
		launch, _ := templates.LaunchForPlatform(template, s.hostOS)
		result.NativeCompatibility = template.Compatibility
		result.Provisionable = true
		result.Summary = "Native SteamCMD installation and structured launch are available"
		result.Installer = templates.InstallerSteamCMD
		result.AppID = plan.AppID
		result.Validate = plan.Validate
		result.LaunchExecutable = launch.Executable
	}
	if result.ContainerCompatibility.Status == templates.Unsupported || result.ContainerCompatibility.Status == "blocked_by_image_policy" {
		if !result.Provisionable {
			result.Provisionable = false
		}
	}
	return result, nil
}

func provisionabilityFailure(err error) (string, string) {
	if code := templates.ValidationCode(err); code != templates.CodeSchemaInvalid {
		var validation *templates.ValidationError
		if errors.As(err, &validation) {
			return code, validation.Message
		}
	}
	return "TEMPLATE_NOT_PROVISIONABLE", "Template is compatible but not provisionable on this node"
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

func (s *Service) execute(ctx context.Context, current *run, template templates.Template, values map[string]string, sensitive map[string]bool, provisionedPorts []ports.Port, plan steamcmd.InstallPlan, runtimeType string, containerPlan *templates.ContainerEggRuntimePlan, selectedImage string, request Request) {
	s.log.Info("provisioning worker started", "module", "Provisioning.Worker", "job_id", current.job.ID, "template_id", template.ID, "recovering", current.recovering)
	defer s.wg.Done()
	defer s.release(current)
	var err error
	created := false
	if ctx.Err() != nil {
		s.finish(current, Cancelled, "Provisioning was cancelled", "", false, "")
		return
	}
	if current.recovering {
		s.installationCompleted(current)
		s.phase(current, ValidatingInstallation, "Revalidating installed game files")
	} else {
		s.phase(current, Preparing, "Preparing managed server storage")
		created, err = prepareRoot(current.root, current.job.ID)
		if err != nil {
			s.log.With("module", "Provisioning.Prepare").Error("managed server storage could not be prepared", "job_id", current.job.ID, "template_id", template.ID, "error", err)
			s.finish(current, Failed, "Provisioning failed", "Server target could not be prepared", false, "")
			return
		}
		if ctx.Err() != nil {
			s.finish(current, Cancelled, "Provisioning was cancelled", "", created, "")
			return
		}
		if runtimeType == RuntimeContainer {
			s.executeContainer(ctx, current, template, values, sensitive, provisionedPorts, containerPlan, selectedImage, request, created, false)
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
	if runtimeType == RuntimeContainer {
		s.executeContainer(ctx, current, template, values, sensitive, provisionedPorts, containerPlan, selectedImage, request, created, current.recovering)
		return
	}
	if !current.recovering {
		s.phase(current, ValidatingInstallation, "Validating installed game files")
	}
	err = templates.ValidateExpectedFiles(template, s.hostOS, values, current.root)
	if err != nil {
		s.log.With("module", "Provisioning.Validation").Error("installed game files failed validation", "job_id", current.job.ID, "template_id", template.ID, "error", err)
		s.fail(current, ValidatingInstallation, templates.ValidationCode(err), "Installation validation failed", "SteamCMD completed, but one or more required launch artifacts were missing or unsafe", true)
		return
	}
	s.phase(current, InstallationValidated, "Game installation validated")
	s.phase(current, ResolvingLaunch, "Applying validated game configuration")
	configSnapshots := make([]servers.ProvisionedConfigAdapter, 0, len(template.ResolvedAdapters))
	managedKeys := map[string]bool{}
	for _, adapter := range template.ResolvedAdapters {
		adapterValues := map[string]string{}
		for _, field := range adapter.Fields {
			if value, ok := values[field.Key]; ok {
				adapterValues[field.Key] = value
			}
		}
		var managedValues []servers.ProvisionedConfigValue
		if gameconfig.ManagedLaunch(adapter) {
			// A managed-launch adapter owns no file. Its initial values are
			// persisted with the server and applied to argv/environment at
			// start, so they must not also become process environment entries.
			initial, valueErr := gameconfig.InitialValues(adapter, adapterValues)
			if valueErr != nil {
				s.log.With("module", "Provisioning.GameConfig").Error("managed launch configuration is invalid", "job_id", current.job.ID, "template_id", template.ID, "adapter_id", adapter.ID, "error", valueErr)
				code, summary := gameConfigFailure(valueErr)
				s.fail(current, ResolvingLaunch, code, "Game configuration failed", summary, true)
				return
			}
			for _, value := range initial {
				managedValues = append(managedValues, servers.ProvisionedConfigValue{Key: value.Key, Value: value.Value, Sensitive: value.Sensitive})
			}
			for _, field := range adapter.Fields {
				managedKeys[field.Key] = true
			}
		} else if !adapter.PostStartOnly {
			if err = gameconfig.Apply(current.root, adapter, adapterValues); err != nil {
				s.log.With("module", "Provisioning.GameConfig").Error("game configuration could not be written", "job_id", current.job.ID, "template_id", template.ID, "adapter_id", adapter.ID, "error", err)
				code, summary := gameConfigFailure(err)
				s.fail(current, ResolvingLaunch, code, "Game configuration failed", summary, true)
				return
			}
		}
		definitionJSON, marshalErr := json.Marshal(adapter)
		if marshalErr != nil {
			s.log.With("module", "Provisioning.GameConfig").Error("game configuration snapshot could not be created", "job_id", current.job.ID, "template_id", template.ID, "adapter_id", adapter.ID, "error", marshalErr)
			s.fail(current, ResolvingLaunch, "LAUNCH_RESOLUTION_FAILED", "Game configuration failed", "Configuration snapshot could not be created", true)
			return
		}
		configSnapshots = append(configSnapshots, servers.ProvisionedConfigAdapter{ID: adapter.ID, SchemaVersion: adapter.SchemaVersion, Version: adapter.Version, TemplateID: template.ID, TemplateVersion: template.Version, DefinitionJSON: definitionJSON, Values: managedValues})
	}
	resolvedLaunch, err := templates.ResolveLaunch(template, s.hostOS, values, current.root)
	if err != nil {
		s.log.With("module", "Provisioning.Validation").Error("native launch resolution failed", "job_id", current.job.ID, "template_id", template.ID, "error", err)
		s.fail(current, ResolvingLaunch, templates.ValidationCode(err), "Launch resolution failed", "Game files and configuration were validated, but GameNode could not resolve the native server launch", true)
		return
	}
	server, metadata, err := buildServer(template, resolvedLaunch, current.job.ServerName, current.job.TenantID, values, sensitive, managedKeys)
	if err != nil {
		s.log.With("module", "Provisioning.ServerConfig").Error("server configuration could not be built", "job_id", current.job.ID, "template_id", template.ID, "error", err)
		s.fail(current, ResolvingLaunch, "LAUNCH_RESOLUTION_FAILED", "Launch resolution failed", "Game files were installed successfully, but GameNode could not resolve the native server launch", true)
		return
	}
	steamCMDInfo := servers.ProvisionedSteamCMD{InstallerType: template.Installer.Type, AppID: plan.AppID, LoginMode: "anonymous", Validate: plan.Validate, BetaBranch: plan.BetaBranch, TemplateID: template.ID, TemplateVersion: template.Version, TemplateSource: template.SourceType}
	safeAdapters, managedSecrets := redactedAdapters(configSnapshots)
	if managedSecrets {
		// Managed secrets must never be written to job state, so this
		// registration cannot be replayed from a snapshot. Persisting a
		// redacted snapshot would let a retry create a server whose managed
		// secrets are silently missing, so no snapshot is written at all and
		// the job is reported as not recoverable.
		s.mu.Lock()
		current.managedSecrets = true
		s.mu.Unlock()
	} else {
		snapshot, marshalErr := json.Marshal(registrationSnapshot{Server: server, TemplateID: template.ID, Variables: metadata, Ports: provisionedPorts, ConfigAdapters: safeAdapters, SteamCMD: steamCMDInfo})
		snapshotErr := marshalErr
		if snapshotErr == nil {
			snapshotErr = s.store.SaveRegistrationSnapshot(context.Background(), current.job.ID, snapshot)
		}
		if snapshotErr != nil {
			s.log.Error("registration snapshot could not be persisted", "module", "Provisioning.Registration", "job_id", current.job.ID, "phase", RegisteringServer, "error_code", "SERVER_RELATED_DATA_FAILED", "error", snapshotErr)
			s.fail(current, RegisteringServer, "SERVER_RELATED_DATA_FAILED", "Server registration failed", "Game files were installed successfully, but GameNode could not prepare the server registration", true)
			return
		}
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
	record, err := s.servers.CreateProvisioned(ctx, server, template.ID, metadata, provisionedPorts, configSnapshots, &steamCMDInfo)
	if err != nil {
		failure, summary := serverCreationFailure(err)
		if managedSecrets {
			summary = "Game files were installed successfully, but the server definition could not be saved. This template has managed secret settings, which GameNode never stores in provisioning job data, so this registration cannot be retried; provision the server again."
		}
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
	s.mu.Lock()
	if _, active := s.active[id]; active {
		s.mu.Unlock()
		return Job{}, ErrJobNotActive
	}
	s.mu.Unlock()
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
	if err = json.Unmarshal(snapshotJSON, &snapshot); err != nil || snapshot.TemplateID != job.TemplateID || snapshot.Server.TenantID != job.TenantID {
		return Job{}, ErrRecoveryUnavailable
	}
	root, err := tenants.TenantServerRoot(s.dataRoot, job.TenantID, job.DirectoryName)
	if err != nil {
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
	current := &run{job: job, root: root}
	s.active[id] = current
	s.mu.Unlock()
	defer s.release(current)
	s.log.Info("retrying persisted server registration", "module", "Provisioning.Retry", "job_id", id, "template_id", job.TemplateID, "phase", RegisteringServer)
	s.reopenRegistration(current)
	exists, err := s.store.ServerExists(ctx, snapshot.Server.ID)
	if err != nil {
		s.fail(current, RegisteringServer, "SERVER_DB_CREATE_FAILED", "Server registration failed", "GameNode could not verify the existing server definition", true)
		return current.job, fmt.Errorf("%w: server verification failed", ErrRecoveryUnavailable)
	}
	if exists {
		s.phase(current, ServerRegistered, "Server registration was already committed")
		s.finish(current, Completed, "Server registered successfully", "", false, snapshot.Server.ID)
		return s.store.Get(ctx, id)
	}
	// A registration snapshot persisted before this metadata existed decodes
	// SteamCMD as a zero value (AppID 0). Rather than reject an otherwise
	// valid retry, register the server without update metadata: it simply
	// stays ineligible for a manual update later, matching how any other
	// pre-existing server without this metadata is treated.
	var steamCMDInfo *servers.ProvisionedSteamCMD
	if snapshot.SteamCMD.AppID > 0 {
		steamCMDInfo = &snapshot.SteamCMD
	}
	record, err := s.servers.CreateProvisioned(ctx, snapshot.Server, snapshot.TemplateID, snapshot.Variables, snapshot.Ports, snapshot.ConfigAdapters, steamCMDInfo)
	if err != nil {
		code, summary := serverCreationFailure(err)
		s.log.Error("retry server registration failed", "module", "Provisioning.Retry", "job_id", id, "template_id", job.TemplateID, "phase", RegisteringServer, "error_code", code, "error", err.Error())
		s.fail(current, RegisteringServer, code, "Server registration failed", summary, true)
		return current.job, fmt.Errorf("%w: %s", ErrRecoveryUnavailable, summary)
	}
	s.phase(current, ServerRegistered, "Server registered successfully")
	s.finish(current, Completed, "Server registered successfully", "", false, record.Server.ID)
	return s.store.Get(ctx, id)
}

func (s *Service) reopenRegistration(current *run) {
	s.mu.Lock()
	now := s.now()
	current.job.Status = RegisteringServer
	current.job.CurrentPhase = RegisteringServer
	current.job.LastSuccessfulPhase = RegisteringServer
	current.job.FailurePhase = ""
	current.job.FailureCode = ""
	current.job.RegistrationRecoverable = false
	current.job.Summary = "Retrying GameNode server registration"
	current.job.ErrorSummary = ""
	current.job.CompletedAt = nil
	current.job.UpdatedAt = now
	job := current.job
	s.mu.Unlock()
	if err := s.store.Update(context.Background(), job); err != nil {
		s.log.Error("registration retry phase could not be persisted", "module", "Provisioning.Retry", "job_id", job.ID, "error", err)
	}
	s.store.Event(context.Background(), job.ID, RegisteringServer, "REGISTRATION_RETRY", job.Summary, now)
}

func serverCreationFailure(err error) (string, string) {
	if errors.Is(err, servers.ErrProvisionedPortConflict) {
		return "SERVER_RELATED_DATA_FAILED", "Game files were installed successfully, but one or more selected ports are already assigned to another GameNode server"
	}
	if errors.Is(err, servers.ErrDuplicateName) {
		return "SERVER_RELATED_DATA_FAILED", "Game files were installed successfully, but the server name is already in use by another GameNode server"
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

func gameConfigFailure(err error) (string, string) {
	switch {
	case errors.Is(err, gameconfig.ErrInitialize):
		return "GAME_CONFIG_INITIALIZATION_FAILED", "Game files were installed, but the managed configuration could not be initialized safely"
	case errors.Is(err, gameconfig.ErrParse):
		return "GAME_CONFIG_PARSE_FAILED", "Game files were installed, but the managed configuration could not be parsed safely"
	default:
		return "GAME_CONFIG_APPLY_FAILED", "Game files were installed, but the validated game configuration could not be written"
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
	// A registration carrying managed secrets has no persisted snapshot, so it
	// must never advertise a retry that would drop those values.
	current.job.RegistrationRecoverable = current.job.InstallationCompleted && phase == RegisteringServer && !current.managedSecrets
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
		eventPhase := job.FailurePhase
		if eventPhase == "" {
			eventPhase = status
		}
		s.store.Event(context.Background(), job.ID, eventPhase, code, summary, now)
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
	if template.Compatibility.Status == templates.Unsupported {
		return steamcmd.InstallPlan{}, ErrNotProvisionable
	}
	if template.Installer.Type != templates.InstallerSteamCMD || template.Installer.SteamCMD == nil {
		return steamcmd.InstallPlan{}, fmt.Errorf("%w: %w", ErrNotProvisionable, &templates.ValidationError{Code: templates.CodeUnsupportedInstaller, Message: "Template installer is not available through managed provisioning"})
	}
	if err := templates.CheckHostRequirements(template, hostOS, runtime.GOARCH); err != nil {
		return steamcmd.InstallPlan{}, fmt.Errorf("%w: %w", ErrNotProvisionable, err)
	}
	if template.Configuration != nil && len(template.ResolvedAdapters) != len(template.Configuration.Adapters) {
		return steamcmd.InstallPlan{}, fmt.Errorf("%w: %w", ErrNotProvisionable, &templates.ValidationError{Code: templates.CodeSchemaInvalid, Message: "Required game configuration adapter is unavailable or invalid"})
	}
	launch, ok := templates.LaunchForPlatform(template, hostOS)
	if !ok {
		return steamcmd.InstallPlan{}, fmt.Errorf("%w: %w", ErrNotProvisionable, &templates.ValidationError{Code: templates.CodeInvalidPlatformLaunch, Message: hostOS + " launch definition missing"})
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

// redactedAdapters strips managed secret values from the persisted job
// registration snapshot and reports whether anything was removed. Secrets must
// never enter job state, so a registration that carries them cannot be replayed
// from a snapshot; the caller marks it non-recoverable instead of creating a
// server whose managed secrets would be silently missing.
func redactedAdapters(adapters []servers.ProvisionedConfigAdapter) ([]servers.ProvisionedConfigAdapter, bool) {
	result := make([]servers.ProvisionedConfigAdapter, 0, len(adapters))
	redacted := false
	for _, adapter := range adapters {
		safe := adapter
		safe.Values = nil
		for _, value := range adapter.Values {
			if value.Sensitive {
				redacted = true
				continue
			}
			safe.Values = append(safe.Values, value)
		}
		result = append(result, safe)
	}
	return result, redacted
}

// buildServer creates the normal native server definition. Keys owned by a
// managed-launch adapter are deliberately excluded from the process
// environment and from template-variable metadata: the adapter snapshot and
// server_config_values are their single source of truth.
func buildServer(template templates.Template, launch templates.ResolvedLaunch, name, tenantID string, values map[string]string, sensitive map[string]bool, managed map[string]bool) (servers.Server, []servers.ProvisionedVariable, error) {
	metadata := make([]servers.ProvisionedVariable, 0, len(values))
	environment := map[string]string{}
	for key, value := range values {
		if managed[key] {
			continue
		}
		environment[key] = value
		metadata = append(metadata, servers.ProvisionedVariable{Key: key, Sensitive: sensitive[key], Source: template.SourceType, Version: template.Version})
	}
	for key, value := range launch.Environment {
		environment[key] = value
	}
	serverID, err := newServerID()
	if err != nil {
		return servers.Server{}, nil, errors.New("server identity could not be created")
	}
	server := servers.Server{ID: serverID, TenantID: tenantID, CreationMode: servers.CreationTemplate, Name: name, Description: template.Description, WorkingDirectory: launch.WorkingDirectory, Executable: launch.Executable, Arguments: launch.Arguments, EnvironmentVariables: environment, RuntimeType: "native", RestartPolicy: "never", StopMethod: launch.StopMethod, StopCommand: launch.StopCommand, StopTimeoutSeconds: launch.StopTimeout, AutoRestartMaxAttempts: 3, AutoRestartWindowSeconds: 300, AutoRestartDelaySeconds: 5}
	if err = server.Validate(); err != nil {
		return servers.Server{}, nil, errors.New("installed server definition is invalid")
	}
	return server, metadata, nil
}

func resolveTemplatePorts(definitions []templates.TemplatePort, values map[string]string, container bool) ([]ports.Port, error) {
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
		if container {
			candidate.ContainerPort = definition.ContainerPort
			if candidate.ContainerPort == 0 {
				candidate.ContainerPort = value
			}
		}
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
func recoverableTarget(ctx context.Context, store *Store, root, templateID, directory, tenantID, actorID string) error {
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
	if err != nil || job.Status != Failed || !job.FilesMayRemain || job.TemplateID != templateID || job.DirectoryName != directory || job.TenantID != tenantID || job.ActorUserID != actorID {
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
func newServerID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	encoded := hex.EncodeToString(raw)
	return encoded[0:8] + "-" + encoded[8:12] + "-4" + encoded[13:16] + "-a" + encoded[17:20] + "-" + encoded[20:], nil
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
