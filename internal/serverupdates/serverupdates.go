// Package serverupdates implements v0.2.1's manual SteamCMD server update
// feature: a single, operator-triggered re-run of SteamCMD's "+app_update"
// against an already-provisioned server's existing managed root.
//
// This is deliberately not a second provisioning engine. It never creates a
// server record, never re-resolves a template, never touches ports or
// configuration adapters, and never reads the live template catalog: the App
// ID, login mode, validate default, and template provenance it acts on come
// exclusively from the trusted snapshot persisted at provisioning time (see
// servers.ProvisionedSteamCMD). A server with no such snapshot is reported as
// ineligible, never reconstructed from directory contents or a freshly
// resolved template. See internal/provisioning for the analogous, larger
// engine this package intentionally does not duplicate.
package serverupdates

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
)

// Phases mirror internal/provisioning's naming convention, scoped to the
// smaller update flow (see spec section 9).
const (
	Pending                = "pending"
	Preparing              = "preparing"
	DownloadingSteamCMD    = "downloading_steamcmd"
	SteamCMDReady          = "steamcmd_ready"
	Updating               = "updating"
	SteamCMDCompleted      = "steamcmd_completed"
	ValidatingInstallation = "validating_installation"
	Completed              = "completed"
	Failed                 = "failed"
	Cancelled              = "cancelled"

	// Live SteamCMD output is held only in memory, per job, and bounded
	// identically to internal/provisioning's installerOutput: it is never
	// written to server_update_jobs and is discarded on GameNode restart.
	// There is nothing sensitive to redact here (no template variables or
	// managed secrets are ever part of a manual update), unlike
	// provisioning's redacted buffer.
	maxOutputLines = 1000
	maxOutputBytes = 256 << 10
	maxOutputJobs  = 64
)

var (
	// ErrNotEligible is returned when a server has no trusted persisted
	// SteamCMD provenance (servers.ProvisionedSteamCMD). This covers custom
	// and adopted servers, non-SteamCMD templates, and servers provisioned
	// before this metadata existed.
	ErrNotEligible = errors.New("server is not eligible for a manual SteamCMD update")
	// ErrServerNotStopped is returned when the target server is not in the
	// stopped state. v0.2.1 never stops a server automatically.
	ErrServerNotStopped = errors.New("server must be stopped before it can be updated")
	// ErrJobNotActive is returned by Cancel when the job is not currently
	// running (already terminal, unknown, or owned by someone else).
	ErrJobNotActive = errors.New("server update job is not active")
	// ErrTargetConflict is returned by Start when another update job is
	// already active for the same server.
	ErrTargetConflict = errors.New("a server update is already in progress")
	// ErrLaunchExecutableMissing is returned when SteamCMD exits successfully
	// but the persisted launch executable no longer safely exists afterward.
	ErrLaunchExecutableMissing = servers.ErrLaunchExecutableMissing
)

// Job is the persisted, API-visible state of one manual update. Only bounded,
// safe fields are ever populated here: no raw SteamCMD output, command
// lines, secrets, or absolute host paths (see spec section 9).
type Job struct {
	ID              string     `json:"id"`
	ServerID        string     `json:"server_id"`
	TenantID        string     `json:"tenant_id"`
	TemplateID      string     `json:"template_id"`
	TemplateVersion string     `json:"template_version"`
	AppID           int        `json:"app_id"`
	Validate        bool       `json:"validate"`
	Status          string     `json:"status"`
	CurrentPhase    string     `json:"current_phase"`
	Summary         string     `json:"summary"`
	ErrorSummary    string     `json:"error_summary,omitempty"`
	ErrorCode       string     `json:"error_code,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ActorUserID     string     `json:"-"`
	ActorUsername   string     `json:"-"`
	Events          []JobEvent `json:"events,omitempty"`
	// InstallerOutput is live SteamCMD stdout/stderr, held only in memory and
	// bounded (see maxOutputLines/maxOutputBytes). It gives an operator a
	// sense of progress/ETA while an update runs; it is never persisted to
	// server_update_jobs and is empty again after a GameNode restart.
	InstallerOutput []string `json:"installer_output,omitempty"`
	OutputTruncated bool     `json:"output_truncated,omitempty"`
}

type JobEvent struct {
	OccurredAt time.Time `json:"occurred_at"`
	Phase      string    `json:"phase"`
	Code       string    `json:"code"`
	Summary    string    `json:"summary"`
}

// Eligibility is the safe, API-visible answer to "can this server be
// manually updated right now, and why not". It never exposes the server
// root, executable path, or a command line.
type Eligibility struct {
	Eligible        bool   `json:"eligible"`
	Reason          string `json:"reason,omitempty"`
	Installer       string `json:"installer,omitempty"`
	AppID           int    `json:"app_id,omitempty"`
	Validate        bool   `json:"validate"`
	TemplateID      string `json:"template_id,omitempty"`
	TemplateVersion string `json:"template_version,omitempty"`
	ServerState     string `json:"server_state,omitempty"`
	ActiveJob       *Job   `json:"active_job,omitempty"`
}

// Event is delivered to an Observer once per terminal job outcome, mirroring
// internal/provisioning.Event/Observer for consistent audit wiring.
type Event struct {
	Action   string
	Job      Job
	Duration time.Duration
}
type Observer func(Event)

// Installer is satisfied directly by *steamcmd.Manager. Re-running "install"
// against an existing root is SteamCMD's native update behavior (+app_update
// installs or updates in place), so no second argv builder is needed.
type Installer interface {
	Install(ctx context.Context, root string, plan steamcmd.InstallPlan, output io.Writer, sink steamcmd.EventSink) error
}

// ServerState is the minimum surface serverupdates needs from
// internal/servers. It is satisfied directly by *servers.Service; the
// interface exists only to keep this package's tests independent of the
// servers package's full native-runtime wiring.
type ServerState interface {
	Get(ctx context.Context, id string) (servers.Record, error)
	SteamCMDProvisioning(ctx context.Context, id string) (servers.ProvisionedSteamCMD, bool, error)
	BeginUpdate(id string) (func(), error)
	VerifyLaunchExecutablePresent(record servers.Record) error
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO server_update_jobs(id,server_id,tenant_id,actor_user_id,actor_username,template_id,template_version,app_id,validate,status,current_phase,summary,error_summary,error_code,created_at,started_at,completed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.ServerID, j.TenantID, j.ActorUserID, j.ActorUsername, j.TemplateID, j.TemplateVersion, j.AppID, j.Validate, j.Status, j.CurrentPhase, j.Summary, j.ErrorSummary, j.ErrorCode, stamp(j.CreatedAt), nullableTime(j.StartedAt), nullableTime(j.CompletedAt), stamp(j.UpdatedAt))
	return err
}

func (s *Store) Update(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `UPDATE server_update_jobs SET status=?,current_phase=?,summary=?,error_summary=?,error_code=?,started_at=?,completed_at=?,updated_at=? WHERE id=?`,
		j.Status, j.CurrentPhase, j.Summary, j.ErrorSummary, j.ErrorCode, nullableTime(j.StartedAt), nullableTime(j.CompletedAt), stamp(j.UpdatedAt), j.ID)
	return err
}

func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	var j Job
	var created, updated string
	var started, completed sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id,server_id,tenant_id,actor_user_id,actor_username,template_id,template_version,app_id,validate,status,current_phase,summary,error_summary,error_code,created_at,started_at,completed_at,updated_at FROM server_update_jobs WHERE id=?`, id).
		Scan(&j.ID, &j.ServerID, &j.TenantID, &j.ActorUserID, &j.ActorUsername, &j.TemplateID, &j.TemplateVersion, &j.AppID, &j.Validate, &j.Status, &j.CurrentPhase, &j.Summary, &j.ErrorSummary, &j.ErrorCode, &created, &started, &completed, &updated)
	if err != nil {
		return Job{}, err
	}
	j.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	j.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	j.StartedAt = parseTime(started)
	j.CompletedAt = parseTime(completed)
	return j, nil
}

// ActiveForServer reports whether a non-terminal job already exists for the
// given server. This is defensive: servers.Service.BeginUpdate already
// prevents two updates for the same server from running concurrently, but a
// crashed/restarted process could in principle leave a stale in-memory
// reservation gap, so Start also checks persisted state directly.
func (s *Store) ActiveForServer(ctx context.Context, serverID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_update_jobs WHERE server_id=? AND status NOT IN ('completed','failed','cancelled')`, serverID).Scan(&count)
	return count > 0, err
}

func (s *Store) Event(ctx context.Context, id, phase, code, summary string, at time.Time) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO server_update_job_events(job_id,occurred_at,phase,code,summary) SELECT ?,?,?,?,? WHERE (SELECT COUNT(*) FROM server_update_job_events WHERE job_id=?) < 200`, id, stamp(at), phase, code, summary, id)
}

func (s *Store) Events(ctx context.Context, id string) ([]JobEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT occurred_at,phase,code,summary FROM server_update_job_events WHERE job_id=? ORDER BY id`, id)
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

// InterruptActive marks any non-terminal job failed on startup. GameNode
// restarting mid-update never resumes SteamCMD and never auto-starts the
// server; a fresh manual update may be started later (spec section 18).
func (s *Store) InterruptActive(ctx context.Context) error {
	now := stamp(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `UPDATE server_update_jobs SET status='failed',current_phase='failed',summary='Server update was interrupted by a GameNode restart',error_summary='GameNode restarted while SteamCMD was running; installed files may reflect a partial update',error_code='INTERRUPTED',completed_at=?,updated_at=? WHERE status IN ('pending','preparing','downloading_steamcmd','steamcmd_ready','updating','steamcmd_completed','validating_installation')`, now, now)
	return err
}

type run struct {
	cancel  context.CancelFunc
	once    sync.Once
	job     Job
	root    string
	release func()
}

// installerOutput is the in-memory, bounded live-output buffer for one job.
// It mirrors internal/provisioning.installerOutput exactly (same caps), just
// without value redaction, which a manual update never needs.
type installerOutput struct {
	lines     []string
	pending   string
	bytes     int
	truncated bool
}

type Options struct {
	Log *slog.Logger
}

type Service struct {
	store       *Store
	servers     ServerState
	installer   Installer
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	wg          sync.WaitGroup
	closed      bool
	active      map[string]*run
	observer    Observer
	now         func() time.Time
	log         *slog.Logger
	outputs     map[string]*installerOutput
	outputOrder []string
}

func New(db *sql.DB, serverState ServerState, installer Installer) *Service {
	return NewWithOptions(db, serverState, installer, Options{})
}

func NewWithOptions(db *sql.DB, serverState ServerState, installer Installer, options Options) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	log := options.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{store: NewStore(db), servers: serverState, installer: installer, ctx: ctx, cancel: cancel, active: map[string]*run{}, outputs: map[string]*installerOutput{}, now: func() time.Time { return time.Now().UTC() }, log: log}
}

func (s *Service) SetObserver(observer Observer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = observer
}

// Initialize marks any job left active by a previous, interrupted GameNode
// process as failed. Call once at startup before serving requests.
func (s *Service) Initialize(ctx context.Context) error {
	s.log.Info("server update recovery started", "module", "ServerUpdates.Recovery")
	if err := s.store.InterruptActive(ctx); err != nil {
		s.log.Error("server update recovery failed", "module", "ServerUpdates.Recovery", "error", err)
		return err
	}
	s.log.Info("server update recovery completed", "module", "ServerUpdates.Recovery")
	return nil
}

func (s *Service) Close() {
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
}

// Status reports whether a server is currently eligible for a manual update
// and its current/active job, if any.
func (s *Service) Status(ctx context.Context, serverID string) (Eligibility, error) {
	info, ok, err := s.servers.SteamCMDProvisioning(ctx, serverID)
	if err != nil {
		return Eligibility{}, err
	}
	if !ok {
		return Eligibility{Eligible: false, Reason: "This server has no trusted SteamCMD provisioning record and cannot be updated through GameNode."}, nil
	}
	record, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return Eligibility{}, err
	}
	active, err := s.activeJob(ctx, serverID)
	if err != nil {
		return Eligibility{}, err
	}
	eligibility := Eligibility{Eligible: true, Installer: info.InstallerType, AppID: info.AppID, Validate: info.Validate, TemplateID: info.TemplateID, TemplateVersion: info.TemplateVersion, ServerState: record.Runtime.CurrentState, ActiveJob: active}
	if active != nil {
		eligibility.Eligible = false
		eligibility.Reason = "A server update is already in progress."
	} else if record.Runtime.CurrentState != servers.StateStopped {
		eligibility.Eligible = false
		eligibility.Reason = "Stop the server before updating."
	}
	return eligibility, nil
}

func (s *Service) activeJob(ctx context.Context, serverID string) (*Job, error) {
	active, err := s.store.ActiveForServer(ctx, serverID)
	if err != nil || !active {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id FROM server_update_jobs WHERE server_id=? AND status NOT IN ('completed','failed','cancelled') ORDER BY created_at DESC LIMIT 1`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var id string
	if err = rows.Scan(&id); err != nil {
		return nil, err
	}
	job, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// Start begins a manual update. The server is reserved (see
// servers.Service.BeginUpdate) before any state is re-checked, closing the
// race window against a concurrent Start/Restart/Delete for the same server.
func (s *Service) Start(ctx context.Context, serverID, actorID, actorUsername string) (Job, error) {
	s.log.Info("server update requested", "module", "ServerUpdates.Start", "server_id", serverID, "actor_user_id", actorID)
	if strings.TrimSpace(actorID) == "" {
		return Job{}, errors.New("update actor is required")
	}
	info, ok, err := s.servers.SteamCMDProvisioning(ctx, serverID)
	if err != nil {
		return Job{}, err
	}
	if !ok {
		return Job{}, ErrNotEligible
	}
	release, err := s.servers.BeginUpdate(serverID)
	if err != nil {
		return Job{}, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			release()
		}
	}()
	record, err := s.servers.Get(ctx, serverID)
	if err != nil {
		return Job{}, err
	}
	if record.Runtime.CurrentState != servers.StateStopped {
		return Job{}, ErrServerNotStopped
	}
	if active, activeErr := s.store.ActiveForServer(ctx, serverID); activeErr != nil {
		return Job{}, activeErr
	} else if active {
		return Job{}, ErrTargetConflict
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Job{}, errors.New("server update service is shutting down")
	}
	id, err := newID()
	if err != nil {
		s.mu.Unlock()
		return Job{}, err
	}
	now := s.now()
	job := Job{ID: id, ServerID: serverID, TenantID: record.Server.TenantID, ActorUserID: actorID, ActorUsername: actorUsername, TemplateID: info.TemplateID, TemplateVersion: info.TemplateVersion, AppID: info.AppID, Validate: info.Validate, Status: Pending, CurrentPhase: Pending, Summary: "Server update is queued", CreatedAt: now, UpdatedAt: now}
	if err = s.store.Create(ctx, job); err != nil {
		s.mu.Unlock()
		return Job{}, err
	}
	jobCtx, cancel := context.WithCancel(s.ctx)
	current := &run{cancel: cancel, job: job, root: record.Server.WorkingDirectory, release: release}
	s.active[id] = current
	s.outputs[id] = &installerOutput{}
	s.outputOrder = append(s.outputOrder, id)
	s.pruneOutputsLocked()
	s.wg.Add(1)
	s.mu.Unlock()
	succeeded = true
	plan := steamcmd.InstallPlan{AppID: info.AppID, Validate: info.Validate, BetaBranch: info.BetaBranch, LoginMode: "anonymous"}
	s.log.Info("server update job queued", "module", "ServerUpdates.Start", "job_id", job.ID, "server_id", serverID, "app_id", info.AppID)
	go s.execute(jobCtx, current, serverID, plan)
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

// Cancel stops an in-flight update. It cannot roll back files SteamCMD has
// already changed; the job's terminal summary makes that explicit.
func (s *Service) Cancel(ctx context.Context, id, actorID string) (Job, error) {
	s.mu.Lock()
	current, ok := s.active[id]
	if !ok || current.job.ActorUserID != actorID {
		s.mu.Unlock()
		return Job{}, ErrJobNotActive
	}
	current.cancel()
	s.mu.Unlock()
	s.finish(current, Cancelled, "Server update was cancelled", "Files already changed by SteamCMD before cancellation were not rolled back.")
	return s.store.Get(ctx, id)
}

func (s *Service) execute(ctx context.Context, current *run, serverID string, plan steamcmd.InstallPlan) {
	defer s.wg.Done()
	defer s.release(current)
	if ctx.Err() != nil {
		s.finish(current, Cancelled, "Server update was cancelled", "")
		return
	}
	s.phase(current, Preparing, "Preparing to update server files")
	err := s.installer.Install(ctx, current.root, plan, s.outputWriter(current.job.ID), func(event steamcmd.Event) {
		switch event.Phase {
		case "downloading_steamcmd":
			s.phase(current, DownloadingSteamCMD, event.Summary)
		case "steamcmd_ready":
			s.phase(current, SteamCMDReady, event.Summary)
		case "installing":
			s.phase(current, Updating, event.Summary)
		}
	})
	if ctx.Err() != nil {
		s.finish(current, Cancelled, "Server update was cancelled", "Files already changed by SteamCMD before cancellation were not rolled back.")
		return
	}
	if err != nil {
		s.log.With("module", "ServerUpdates.Execute").Error("SteamCMD update failed", "job_id", current.job.ID, "server_id", serverID, "app_id", plan.AppID, "error", err)
		s.fail(current, Updating, "STEAMCMD_UPDATE_FAILED", "Server update failed", "SteamCMD could not update the game; installed files may reflect a partial update")
		return
	}
	s.phase(current, SteamCMDCompleted, "SteamCMD completed successfully")
	s.phase(current, ValidatingInstallation, "Validating installed game files")
	record, err := s.servers.Get(ctx, serverID)
	if err != nil {
		s.fail(current, ValidatingInstallation, "SERVER_LOOKUP_FAILED", "Server update failed", "The server could not be re-verified after the update")
		return
	}
	if err = s.servers.VerifyLaunchExecutablePresent(record); err != nil {
		s.log.With("module", "ServerUpdates.Validation").Error("launch executable missing after update", "job_id", current.job.ID, "server_id", serverID, "error", err)
		s.fail(current, ValidatingInstallation, "LAUNCH_EXECUTABLE_MISSING", "Server update failed", "SteamCMD completed, but the server's launch executable was missing or unsafe afterward")
		return
	}
	s.finish(current, Completed, "Server updated successfully", "")
	s.log.With("module", "ServerUpdates.Complete").Info("server update completed", "job_id", current.job.ID, "server_id", serverID, "app_id", plan.AppID)
}

func (s *Service) phase(current *run, status, summary string) {
	s.mu.Lock()
	if terminal(current.job.Status) {
		s.mu.Unlock()
		return
	}
	current.job.Status = status
	current.job.CurrentPhase = status
	current.job.Summary = summary
	now := s.now()
	if current.job.StartedAt == nil {
		current.job.StartedAt = &now
	}
	current.job.UpdatedAt = now
	job := current.job
	s.mu.Unlock()
	if err := s.store.Update(context.Background(), job); err != nil {
		s.log.Error("server update phase could not be persisted", "module", "ServerUpdates.Phase", "job_id", job.ID, "phase", status, "error", err)
	}
	s.store.Event(context.Background(), job.ID, status, "PHASE_CHANGED", summary, now)
}

func (s *Service) fail(current *run, phase, code, summary, errorSummary string) {
	s.mu.Lock()
	current.job.CurrentPhase = phase
	current.job.ErrorCode = code
	s.mu.Unlock()
	s.finish(current, Failed, summary, errorSummary)
}

// finish transitions a job to its terminal state exactly once, releases the
// server reservation, and notifies the observer (used by internal/api to
// record one bounded, sanitized audit event per outcome).
func (s *Service) finish(current *run, status, summary, errorSummary string) {
	current.once.Do(func() {
		s.mu.Lock()
		now := s.now()
		current.job.Status = status
		current.job.CurrentPhase = status
		current.job.Summary = summary
		current.job.ErrorSummary = errorSummary
		if status == Failed {
			current.job.ErrorCode = orDefault(current.job.ErrorCode, "UPDATE_FAILED")
		}
		current.job.CompletedAt = &now
		current.job.UpdatedAt = now
		job := current.job
		observer := s.observer
		s.mu.Unlock()
		if err := s.store.Update(context.Background(), job); err != nil {
			s.log.Error("terminal server update state could not be persisted", "module", "ServerUpdates.Complete", "job_id", job.ID, "status", status, "error", err)
		}
		code := map[string]string{Completed: "JOB_COMPLETED", Failed: "JOB_FAILED", Cancelled: "JOB_CANCELLED"}[status]
		s.store.Event(context.Background(), job.ID, status, code, summary, now)
		if observer != nil {
			action := map[string]string{Completed: "server.steamcmd_update_complete", Failed: "server.steamcmd_update_fail", Cancelled: "server.steamcmd_update_cancel"}[status]
			observer(Event{Action: action, Job: job, Duration: now.Sub(job.CreatedAt)})
		}
		if current.release != nil {
			current.release()
		}
		s.log.Info("server update job finished", "module", "ServerUpdates.Complete", "job_id", job.ID, "server_id", job.ServerID, "status", status)
	})
}

func (s *Service) release(current *run) {
	s.mu.Lock()
	delete(s.active, current.job.ID)
	s.mu.Unlock()
}

type outputWriter struct {
	service *Service
	jobID   string
}

func (w outputWriter) Write(value []byte) (int, error) {
	w.service.appendOutput(w.jobID, string(value))
	return len(value), nil
}

func (s *Service) outputWriter(jobID string) io.Writer {
	return outputWriter{service: s, jobID: jobID}
}

// appendOutput mirrors internal/provisioning's line-splitting/bounding logic
// exactly, minus the value-redaction pass (a manual update never carries
// template variables or managed secrets).
func (s *Service) appendOutput(jobID, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := s.outputs[jobID]
	if output == nil || output.truncated {
		return
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

func (s *Service) appendOutputLine(output *installerOutput, line string) {
	if output.truncated {
		return
	}
	if len(output.lines) >= maxOutputLines || output.bytes+len(line) > maxOutputBytes {
		output.truncated = true
		return
	}
	output.lines = append(output.lines, line)
	output.bytes += len(line)
}

// pruneOutputsLocked bounds total in-memory output buffers across jobs,
// mirroring internal/provisioning's identical cap. Callers must hold s.mu.
func (s *Service) pruneOutputsLocked() {
	for len(s.outputOrder) > maxOutputJobs {
		id := s.outputOrder[0]
		s.outputOrder = s.outputOrder[1:]
		delete(s.outputs, id)
	}
}

func terminal(status string) bool {
	return status == Completed || status == Failed || status == Cancelled
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
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
