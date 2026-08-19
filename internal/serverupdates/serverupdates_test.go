package serverupdates

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/console"
	"gamenode/internal/database"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
)

// stubRuntime is a minimal runtime.Runtime that never actually starts a
// process. Every test server stays stopped, which is exactly the state a
// manual update requires, so no test needs a working native runtime.
type stubRuntime struct{}

func (stubRuntime) Start(context.Context, gameRuntime.StartOptions) (gameRuntime.Identity, <-chan gameRuntime.ExitResult, error) {
	return gameRuntime.Identity{}, nil, errors.New("stubRuntime does not start processes")
}
func (stubRuntime) Stop(context.Context, gameRuntime.Identity, time.Duration) error { return nil }
func (stubRuntime) Interrupt(context.Context, gameRuntime.Identity) error           { return nil }
func (stubRuntime) Kill(context.Context, gameRuntime.Identity) error                { return nil }
func (stubRuntime) Status(context.Context, gameRuntime.Identity) (gameRuntime.Status, error) {
	return gameRuntime.Status{}, nil
}
func (stubRuntime) Metrics(context.Context, gameRuntime.Identity) (gameRuntime.Metrics, error) {
	return gameRuntime.Metrics{}, nil
}

type fakeInstaller struct {
	calls   atomic.Int32
	err     error
	wait    chan struct{}
	started chan struct{}
	plan    steamcmd.InstallPlan
	// removeExecutable, if set, is deleted from root right before Install
	// returns successfully - simulating SteamCMD leaving the launch
	// executable missing after an otherwise successful update.
	removeExecutable string
	// output, if set, is written to the installer's output writer, simulating
	// live SteamCMD stdout.
	output string
}

func (f *fakeInstaller) Install(ctx context.Context, root string, plan steamcmd.InstallPlan, output io.Writer, sink steamcmd.EventSink) error {
	f.calls.Add(1)
	f.plan = plan
	if output != nil && f.output != "" {
		_, _ = io.WriteString(output, f.output)
	}
	if sink != nil {
		sink(steamcmd.Event{Phase: "installing", Summary: "Updating game"})
	}
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.wait != nil {
		select {
		case <-f.wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.err != nil {
		return f.err
	}
	if f.removeExecutable != "" {
		if err := os.Remove(filepath.Join(root, f.removeExecutable)); err != nil {
			return err
		}
	}
	return nil
}

func testHarness(t *testing.T) (*servers.Service, *sql.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	return servers.NewService(servers.NewStore(db), stubRuntime{}, console.NewManager()), db
}

// eligibleServer creates a stopped, SteamCMD-provisioned server with a real
// launch executable inside a temp root, so post-update validation has a real
// file to check.
var eligibleServerSeq atomic.Int32

func eligibleServer(t *testing.T, serverService *servers.Service, appID int, validate bool) servers.Record {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "server.bin")
	if err := os.WriteFile(executable, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	name := "eligible-" + t.Name() + "-" + strconv.Itoa(int(eligibleServerSeq.Add(1)))
	server := servers.Server{Name: name, CreationMode: servers.CreationTemplate, WorkingDirectory: root, Executable: "server.bin", RuntimeType: "native", Arguments: []string{}, EnvironmentVariables: map[string]string{}, StopTimeoutSeconds: 1}
	steamCMD := &servers.ProvisionedSteamCMD{InstallerType: "steamcmd", AppID: appID, LoginMode: "anonymous", Validate: validate, TemplateID: "project-zomboid", TemplateVersion: "2.0.0", TemplateSource: "official"}
	record, err := serverService.CreateProvisioned(context.Background(), server, "project-zomboid", nil, nil, nil, steamCMD)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func ineligibleServer(t *testing.T, serverService *servers.Service) servers.Record {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "server.bin")
	if err := os.WriteFile(executable, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	record, err := serverService.Create(context.Background(), servers.Server{Name: "custom-" + t.Name(), CreationMode: servers.CreationCustom, WorkingDirectory: root, Executable: "server.bin", RuntimeType: "native", StopTimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func waitForTerminal(t *testing.T, service *Service, jobID string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := service.Get(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		if terminal(job.Status) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job never reached a terminal state")
	return Job{}
}

func TestSuccessfulUpdateUsesTrustedPlanAndCompletes(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 380870, true)
	installer := &fakeInstaller{}
	service := New(db, serverService, installer)
	defer service.Close()

	job, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	job = waitForTerminal(t, service, job.ID)
	if job.Status != Completed {
		t.Fatalf("expected completed, got %s (%s)", job.Status, job.ErrorSummary)
	}
	if installer.calls.Load() != 1 {
		t.Fatalf("expected exactly one Install call, got %d", installer.calls.Load())
	}
	// The App ID/validate/login mode used must come from the trusted
	// persisted snapshot, never any user input.
	if installer.plan.AppID != 380870 || !installer.plan.Validate || installer.plan.LoginMode != "anonymous" || installer.plan.BetaBranch != "" {
		t.Fatalf("unexpected install plan: %#v", installer.plan)
	}
	if job.AppID != 380870 || job.TemplateID != "project-zomboid" || job.TemplateVersion != "2.0.0" || !job.Validate {
		t.Fatalf("job does not reflect trusted metadata: %#v", job)
	}
}

func TestValidateFlagDisabledIsHonored(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 108600, false)
	installer := &fakeInstaller{}
	service := New(db, serverService, installer)
	defer service.Close()

	job, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminal(t, service, job.ID)
	if installer.plan.Validate {
		t.Fatal("expected validate to remain disabled")
	}
}

func TestSteamCMDFailureMarksJobFailed(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 380870, false)
	installer := &fakeInstaller{err: errors.New("steamcmd process failed")}
	service := New(db, serverService, installer)
	defer service.Close()

	job, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	job = waitForTerminal(t, service, job.ID)
	if job.Status != Failed || job.ErrorCode == "" {
		t.Fatalf("expected a sanitized failure, got %#v", job)
	}
	if job.ErrorSummary == "" {
		t.Fatal("expected a bounded error summary")
	}
}

func TestPostUpdateMissingExecutableFailsUpdate(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 380870, false)
	installer := &fakeInstaller{removeExecutable: "server.bin"}
	service := New(db, serverService, installer)
	defer service.Close()

	job, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	job = waitForTerminal(t, service, job.ID)
	if job.Status != Failed || job.ErrorCode != "LAUNCH_EXECUTABLE_MISSING" {
		t.Fatalf("expected a launch-executable-missing failure, got %#v", job)
	}
}

func TestCancellationStopsSteamCMDAndFinalizesExactlyOnce(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 380870, false)
	installer := &fakeInstaller{wait: make(chan struct{}), started: make(chan struct{}, 1)}
	service := New(db, serverService, installer)
	defer service.Close()

	job, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	<-installer.started
	if _, err = service.Cancel(context.Background(), job.ID, "actor-1"); err != nil {
		t.Fatal(err)
	}
	job = waitForTerminal(t, service, job.ID)
	if job.Status != Cancelled {
		t.Fatalf("expected cancelled, got %s", job.Status)
	}
	// A second cancel must not be accepted as if it did something new.
	if _, err = service.Cancel(context.Background(), job.ID, "actor-1"); !errors.Is(err, ErrJobNotActive) {
		t.Fatalf("expected ErrJobNotActive on a repeat cancel, got %v", err)
	}
	// The server reservation must have been released so a fresh update can start.
	if release, err := serverService.BeginUpdate(record.Server.ID); err != nil {
		t.Fatalf("expected the server reservation to be released after cancellation: %v", err)
	} else {
		release()
	}
}

func TestRunningServerIsRejected(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	// A running server cannot use stubRuntime.Start (it errors), so simulate
	// "running" by writing runtime state directly - the eligibility/Start
	// checks only ever read persisted state, never actually launch anything.
	record := eligibleServer(t, serverService, 380870, false)
	if _, err := db.Exec(`UPDATE server_runtime_state SET current_state='running' WHERE server_id=?`, record.Server.ID); err != nil {
		t.Fatal(err)
	}
	installer := &fakeInstaller{}
	service := New(db, serverService, installer)
	defer service.Close()

	if _, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor"); !errors.Is(err, ErrServerNotStopped) {
		t.Fatalf("expected ErrServerNotStopped, got %v", err)
	}
}

func TestIneligibleServerIsRejected(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := ineligibleServer(t, serverService)
	installer := &fakeInstaller{}
	service := New(db, serverService, installer)
	defer service.Close()

	if _, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor"); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("expected ErrNotEligible, got %v", err)
	}
}

func TestConcurrentSameServerUpdateRejected(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 380870, false)
	installer := &fakeInstaller{wait: make(chan struct{}), started: make(chan struct{}, 1)}
	service := New(db, serverService, installer)
	defer service.Close()

	job, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	<-installer.started
	if _, err = service.Start(context.Background(), record.Server.ID, "actor-2", "actor"); err == nil {
		t.Fatal("expected the second concurrent update to be rejected")
	}
	close(installer.wait)
	waitForTerminal(t, service, job.ID)
}

func TestIndependentServersUpdateConcurrently(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	first := eligibleServer(t, serverService, 380870, false)
	second := eligibleServer(t, serverService, 108600, false)
	installer := &fakeInstaller{}
	service := New(db, serverService, installer)
	defer service.Close()

	jobA, err := service.Start(context.Background(), first.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	jobB, err := service.Start(context.Background(), second.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatalf("independent server update should not be blocked: %v", err)
	}
	waitForTerminal(t, service, jobA.ID)
	waitForTerminal(t, service, jobB.ID)
}

func TestInterruptActiveMarksNonTerminalJobsFailed(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 380870, false)
	service := New(db, serverService, &fakeInstaller{})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO server_update_jobs(id,server_id,tenant_id,actor_user_id,actor_username,template_id,template_version,app_id,validate,status,current_phase,summary,error_summary,error_code,created_at,started_at,completed_at,updated_at) VALUES('stale-job',?,?,'actor','actor','project-zomboid','2.0.0',380870,0,'updating','updating','In progress','','',?,?,NULL,?)`, record.Server.ID, "default", now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := service.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := service.Get(context.Background(), "stale-job")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != Failed || job.ErrorCode != "INTERRUPTED" {
		t.Fatalf("expected the stale job to be marked failed/interrupted, got %#v", job)
	}
}

func TestLiveInstallerOutputSurfacesThroughGetAndIsNeverPersisted(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 380870, false)
	installer := &fakeInstaller{output: "Steam> Downloading depot 1234...\nSteam> Success. App '380870' fully installed.\n"}
	service := New(db, serverService, installer)
	defer service.Close()

	job, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	job = waitForTerminal(t, service, job.ID)
	if job.Status != Completed {
		t.Fatalf("expected completed, got %s", job.Status)
	}
	found, err := service.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, line := range found.InstallerOutput {
		joined += line + "\n"
	}
	if !strings.Contains(joined, "fully installed") {
		t.Fatalf("expected live installer output to surface through Get, got %#v", found.InstallerOutput)
	}
	// The buffer is in-memory only: server_update_jobs must never carry it.
	var columns int
	if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('server_update_jobs') WHERE name IN ('installer_output','output')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("server_update_jobs must never persist raw SteamCMD output")
	}
}

func TestServerDefinitionUnchangedAfterUpdate(t *testing.T) {
	serverService, db := testHarness(t)
	defer db.Close()
	record := eligibleServer(t, serverService, 380870, true)
	before := record.Server
	service := New(db, serverService, &fakeInstaller{})
	defer service.Close()

	job, err := service.Start(context.Background(), record.Server.ID, "actor-1", "actor")
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminal(t, service, job.ID)
	after, err := serverService.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Server.Executable != before.Executable || after.Server.WorkingDirectory != before.WorkingDirectory || len(after.Server.Arguments) != len(before.Arguments) {
		t.Fatalf("server definition changed after update: before=%#v after=%#v", before, after.Server)
	}
	info, ok, err := serverService.SteamCMDProvisioning(context.Background(), record.Server.ID)
	if err != nil || !ok {
		t.Fatalf("expected steamcmd provisioning metadata to remain, ok=%v err=%v", ok, err)
	}
	if info.TemplateVersion != "2.0.0" {
		t.Fatalf("template version must stay pinned, got %s", info.TemplateVersion)
	}
}
