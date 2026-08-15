package provisioning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/database"
	"gamenode/internal/gameconfig"
	"gamenode/internal/ports"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
)

type templateSource struct {
	mu       sync.Mutex
	template templates.Template
	err      error
}

func (s *templateSource) Get(context.Context, string) (templates.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.template, s.err
}

type fakeInstaller struct {
	err              error
	wait             chan struct{}
	started          chan struct{}
	calls            atomic.Int32
	plan             steamcmd.InstallPlan
	createExecutable bool
	executable       string
	configXML        string
	files            map[string][]byte
	output           string
}

func (i *fakeInstaller) Install(ctx context.Context, root string, plan steamcmd.InstallPlan, output io.Writer, sink steamcmd.EventSink) error {
	i.calls.Add(1)
	i.plan = plan
	if output != nil {
		_, _ = io.WriteString(output, "Steam> Downloading depot 1...\nSteam> Success\n")
		if i.output != "" {
			_, _ = io.WriteString(output, i.output)
		}
	}
	if sink != nil {
		sink(steamcmd.Event{Phase: "downloading_steamcmd", Summary: "Downloading SteamCMD"})
		sink(steamcmd.Event{Phase: "steamcmd_ready", Summary: "SteamCMD ready"})
		sink(steamcmd.Event{Phase: "installing", Summary: "Installing game"})
	}
	if i.started != nil {
		select {
		case i.started <- struct{}{}:
		default:
		}
	}
	if i.wait != nil {
		select {
		case <-i.wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if i.err != nil {
		return i.err
	}
	if i.createExecutable {
		executable := i.executable
		if executable == "" {
			executable = "server.x86_64"
		}
		executablePath := filepath.Join(root, filepath.FromSlash(executable))
		if err := os.MkdirAll(filepath.Dir(executablePath), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(executablePath, []byte("test"), 0700); err != nil {
			return err
		}
	}
	if i.configXML != "" {
		if err := os.WriteFile(filepath.Join(root, "serverconfig.xml"), []byte(i.configXML), 0600); err != nil {
			return err
		}
	}
	for relative, data := range i.files {
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

type failingCreator struct{ calls atomic.Int32 }

func (c *failingCreator) CreateProvisioned(context.Context, servers.Server, string, []servers.ProvisionedVariable, []ports.Port, []servers.ProvisionedConfigAdapter) (servers.Record, error) {
	c.calls.Add(1)
	return servers.Record{}, errors.New("database unavailable SECRET")
}

type portConflictCreator struct{}

func (portConflictCreator) CreateProvisioned(context.Context, servers.Server, string, []servers.ProvisionedVariable, []ports.Port, []servers.ProvisionedConfigAdapter) (servers.Record, error) {
	return servers.Record{}, servers.ErrProvisionedPortConflict
}

type blockingCreator struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	next    ServerCreator
}

func (c *blockingCreator) CreateProvisioned(ctx context.Context, server servers.Server, templateID string, variables []servers.ProvisionedVariable, serverPorts []ports.Port, adapters []servers.ProvisionedConfigAdapter) (servers.Record, error) {
	c.calls.Add(1)
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		return servers.Record{}, ctx.Err()
	}
	return c.next.CreateProvisioned(ctx, server, templateID, variables, serverPorts, adapters)
}

func provisionFixture(t *testing.T) (*sql.DB, string, templates.Template) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	min, max := float64(1), float64(65535)
	template := templates.Template{ID: "template", Name: "7 Days To Die", Description: "fixture", SourceType: templates.SourcePelicanPterodactyl, Installer: templates.InstallerDefinition{Type: templates.InstallerSteamCMD, SteamCMD: &templates.SteamCMDPlan{AppID: 294420, Validate: true, LoginMode: "anonymous", Platform: "native", BetaBranchVariable: "BETA", BetaPasswordVariable: "BETA_PASSWORD", InstallTarget: "server_root"}}, Launch: &templates.LaunchDefinition{Executable: "./server.x86_64", Arguments: []string{"-port=${SERVER_PORT}", "-name=${SERVER_NAME}"}, WorkingRoot: "server_root"}, Variables: []templates.TemplateVariable{{Name: "Port", Key: "SERVER_PORT", DefaultValue: "26900", UserEditable: true, Type: "integer", Required: true, Validation: templates.Validation{Min: &min, Max: &max}}, {Name: "Server name", Key: "SERVER_NAME", DefaultValue: "seven", UserEditable: true, Type: "string", Required: true}, {Name: "Beta", Key: "BETA", DefaultValue: "", UserEditable: true, Type: "string", Nullable: true}, {Name: "Beta password", Key: "BETA_PASSWORD", DefaultValue: "", UserEditable: true, Type: "secret", Sensitive: true, Nullable: true}}, Compatibility: templates.Compatibility{Status: templates.PartiallyCompatible}}
	template.Version = "3.0.0"
	return db, root, template
}

func TestProvisioningSuccessCreatesNormalServerAfterInstall(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	source := &templateSource{template: template}
	installer := &fakeInstaller{createExecutable: true}
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	service := NewWithOptions(db, source, installer, serverService, data, Options{HostOS: "linux"})
	defer service.Close()
	var eventsMu sync.Mutex
	var events []string
	service.SetObserver(func(event Event) { eventsMu.Lock(); events = append(events, event.Action); eventsMu.Unlock() })
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "seven", Values: map[string]string{"SERVER_PORT": "26901", "SERVER_NAME": "My Server", "BETA": "latest_experimental"}, ActorUserID: "actor", ActorUsername: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.Status != Completed || job.ServerID == "" {
		t.Fatalf("job=%#v", job)
	}
	if got := strings.Join(job.InstallerOutput, "\n"); !strings.Contains(got, "Downloading depot 1") || !strings.Contains(got, "Success") {
		t.Fatalf("installer output=%q", got)
	}
	record, err := serverService.Get(context.Background(), job.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Server.CreationMode != servers.CreationTemplate || record.Server.Arguments[0] != "-port=26901" || record.Server.Arguments[1] != "-name=My Server" {
		t.Fatalf("server=%#v", record.Server)
	}
	var sourceType, templateVersion string
	if err = db.QueryRow(`SELECT template_source,template_version FROM server_template_variables WHERE server_id=? LIMIT 1`, job.ServerID).Scan(&sourceType, &templateVersion); err != nil || sourceType != templates.SourcePelicanPterodactyl || templateVersion != "3.0.0" {
		t.Fatalf("template provenance source=%q version=%q err=%v", sourceType, templateVersion, err)
	}
	if installer.plan.AppID != 294420 || installer.plan.BetaBranch != "latest_experimental" {
		t.Fatalf("plan=%#v", installer.plan)
	}
	wantPhases := []string{Preparing, DownloadingSteamCMD, SteamCMDReady, Installing, SteamCMDCompleted, ValidatingInstallation, InstallationValidated, ResolvingLaunch, RegisteringServer, ServerRegistered, Completed}
	if len(job.Events) != len(wantPhases) {
		t.Fatalf("event phases=%#v", job.Events)
	}
	for index, phase := range wantPhases {
		if job.Events[index].Phase != phase {
			t.Fatalf("event %d phase=%q want=%q", index, job.Events[index].Phase, phase)
		}
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) != 1 || events[0] != "server.provision_complete" {
		t.Fatalf("events=%#v", events)
	}
}

func TestTemplateDeletionDuringJobDoesNotCorruptSnapshot(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	source := &templateSource{template: template}
	wait := make(chan struct{})
	started := make(chan struct{}, 1)
	installer := &fakeInstaller{createExecutable: true, wait: wait, started: started}
	service := NewWithOptions(db, source, installer, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "deleted-template", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	source.mu.Lock()
	source.err = sql.ErrNoRows
	source.template = templates.Template{}
	source.mu.Unlock()
	close(wait)
	job = waitTerminal(t, service, job.ID)
	if job.Status != Completed || job.ServerID == "" {
		t.Fatalf("job=%#v", job)
	}
}

func TestProvisioningFailuresNeverCreateGhostServer(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*templates.Template, map[string]string, *fakeInstaller)
	}{{"unsupported", func(template *templates.Template, _ map[string]string, _ *fakeInstaller) {
		template.Compatibility.Status = templates.Unsupported
	}}, {"invalid variable", func(_ *templates.Template, values map[string]string, _ *fakeInstaller) { values["UNKNOWN"] = "x" }}, {"invalid beta", func(_ *templates.Template, values map[string]string, _ *fakeInstaller) { values["BETA"] = "+quit" }}, {"beta password unsupported", func(_ *templates.Template, values map[string]string, _ *fakeInstaller) {
		values["BETA_PASSWORD"] = "secret"
	}}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, data, template := provisionFixture(t)
			defer db.Close()
			values := map[string]string{}
			installer := &fakeInstaller{}
			test.mutate(&template, values, installer)
			service := NewWithOptions(db, &templateSource{template: template}, installer, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
			defer service.Close()
			if _, err := service.Start(context.Background(), Request{TemplateID: "template", ServerName: "Seven", DirectoryName: "seven", Values: values, ActorUserID: "actor"}); err == nil {
				t.Fatal("invalid provisioning accepted")
			}
			var count int
			db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&count)
			if count != 0 {
				t.Fatal("ghost server created")
			}
		})
	}
}

func TestInstallFailureAndDBFailureLeaveNoServer(t *testing.T) {
	for _, test := range []struct {
		name      string
		installer *fakeInstaller
		creator   ServerCreator
	}{{"install", &fakeInstaller{err: errors.New("raw secret")}, nil}, {"database", &fakeInstaller{createExecutable: true}, &failingCreator{}}} {
		t.Run(test.name, func(t *testing.T) {
			db, data, template := provisionFixture(t)
			defer db.Close()
			creator := test.creator
			if creator == nil {
				creator = servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
			}
			service := NewWithOptions(db, &templateSource{template: template}, test.installer, creator, data, Options{HostOS: "linux"})
			defer service.Close()
			job, err := service.Start(context.Background(), Request{TemplateID: "template", ServerName: "Seven", DirectoryName: "seven", ActorUserID: "actor"})
			if err != nil {
				t.Fatal(err)
			}
			job = waitTerminal(t, service, job.ID)
			if job.Status != Failed || !job.FilesMayRemain || strings.Contains(job.ErrorSummary, "secret") {
				t.Fatalf("job=%#v", job)
			}
			var count int
			db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&count)
			if count != 0 {
				t.Fatal("ghost server created")
			}
		})
	}
}

func TestRegistrationFailurePreservesCompletedInstallationForRecovery(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	installer := &fakeInstaller{createExecutable: true}
	service := NewWithOptions(db, &templateSource{template: template}, installer, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "recovery", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.FailureCode != "SERVER_DB_CREATE_FAILED" || job.FailurePhase != RegisteringServer || !job.InstallationCompleted || !job.RegistrationRecoverable {
		t.Fatalf("registration failure lost installation state: %#v", job)
	}
	if installer.calls.Load() != 1 {
		t.Fatalf("installer calls=%d", installer.calls.Load())
	}
}

func TestRetryRegistrationReusesSnapshotWithoutRunningInstaller(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	installer := &fakeInstaller{createExecutable: true}
	service := NewWithOptions(db, &templateSource{template: template}, installer, &failingCreator{}, data, Options{HostOS: "linux"})
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "retry", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	service.Close()
	service = NewWithOptions(db, &templateSource{template: template}, installer, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
	defer service.Close()
	if err = service.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	retried, err := service.RetryRegistration(context.Background(), job.ID, "actor")
	if err != nil || retried.Status != Completed || retried.ServerID == "" {
		t.Fatalf("retry=%#v err=%v", retried, err)
	}
	if retried.FailureCode != "" || retried.FailurePhase != "" || retried.RegistrationRecoverable {
		t.Fatalf("retry retained failure state: %#v", retried)
	}
	if installer.calls.Load() != 1 {
		t.Fatalf("retry invoked installer %d times", installer.calls.Load())
	}
	if _, err = service.RetryRegistration(context.Background(), job.ID, "actor"); !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatalf("duplicate retry err=%v", err)
	}
}

func TestConcurrentRetryRegistrationCreatesExactlyOneServer(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	installer := &fakeInstaller{createExecutable: true}
	service := NewWithOptions(db, &templateSource{template: template}, installer, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "concurrent-retry", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	creator := &blockingCreator{started: make(chan struct{}, 1), release: make(chan struct{}), next: servers.NewService(servers.NewStore(db), gameRuntime.NewNative())}
	service.servers = creator
	result := make(chan error, 1)
	go func() {
		_, retryErr := service.RetryRegistration(context.Background(), job.ID, "actor")
		result <- retryErr
	}()
	<-creator.started
	if _, err = service.RetryRegistration(context.Background(), job.ID, "actor"); !errors.Is(err, ErrJobNotActive) {
		t.Fatalf("concurrent retry error=%v", err)
	}
	close(creator.release)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&count); err != nil || count != 1 || creator.calls.Load() != 1 {
		t.Fatalf("servers=%d creator calls=%d err=%v", count, creator.calls.Load(), err)
	}
}

func TestRetryRegistrationAcceptsAlreadyCommittedServer(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	installer := &fakeInstaller{createExecutable: true}
	service := NewWithOptions(db, &templateSource{template: template}, installer, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "committed-retry", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	snapshotJSON, err := service.store.RegistrationSnapshot(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot registrationSnapshot
	if err = json.Unmarshal(snapshotJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	creator := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	if _, err = creator.CreateProvisioned(context.Background(), snapshot.Server, snapshot.TemplateID, snapshot.Variables, snapshot.Ports, snapshot.ConfigAdapters); err != nil {
		t.Fatal(err)
	}
	service.servers = creator
	retried, err := service.RetryRegistration(context.Background(), job.ID, "actor")
	if err != nil || retried.Status != Completed || retried.ServerID != snapshot.Server.ID {
		t.Fatalf("retry=%#v err=%v", retried, err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("servers=%d err=%v", count, err)
	}
}

func TestValidationFailureIsNotReportedAsSteamCMDFailure(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	service := NewWithOptions(db, &templateSource{template: template}, &fakeInstaller{}, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "missing", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.FailureCode != templates.CodeInvalidPath || job.FailurePhase != ResolvingLaunch || !job.InstallationCompleted {
		t.Fatalf("validation result=%#v", job)
	}
}

func TestSteamCMDDiskSpaceOutputGetsStableFailureCode(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	installer := &fakeInstaller{err: steamcmd.ErrInstallFailed, output: "Failed to preallocate (Not enough disk space) 6.41 GB\n"}
	service := NewWithOptions(db, &templateSource{template: template}, installer, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "disk", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.FailureCode != "STEAMCMD_INSUFFICIENT_DISK_SPACE" || job.ErrorSummary != "Not enough free disk space to install this server." {
		t.Fatalf("disk fixture=%#v", job)
	}
}

func TestSteamCMDProcessFailureDoesNotCompleteInstallation(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	service := NewWithOptions(db, &templateSource{template: template}, &fakeInstaller{err: steamcmd.ErrInstallFailed}, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "process-failure", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.FailureCode != "STEAMCMD_PROCESS_FAILED" || job.InstallationCompleted {
		t.Fatalf("process failure=%#v", job)
	}
}

func TestProvisionedPortConflictHasSafeActionableFailure(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	service := NewWithOptions(db, &templateSource{template: template}, &fakeInstaller{createExecutable: true}, portConflictCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Seven", DirectoryName: "seven", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.Status != Failed || !job.FilesMayRemain || job.FailureCode != "SERVER_RELATED_DATA_FAILED" || job.ErrorSummary != "Game files were installed successfully, but one or more selected ports are already assigned to another GameNode server" {
		t.Fatalf("job=%#v", job)
	}
}

func TestCancellationStopsFinalization(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	installer := &fakeInstaller{wait: make(chan struct{}), started: make(chan struct{}, 1), createExecutable: true}
	service := NewWithOptions(db, &templateSource{template: template}, installer, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
	defer service.Close()
	var events atomic.Int32
	service.SetObserver(func(event Event) {
		if event.Action == "server.provision_cancel" {
			events.Add(1)
		}
	})
	job, err := service.Start(context.Background(), Request{TemplateID: "template", ServerName: "Seven", DirectoryName: "seven", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-installer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("installer did not start")
	}
	job, err = service.Cancel(context.Background(), job.ID, "actor")
	if err != nil || job.Status != Cancelled {
		t.Fatalf("cancel=%#v %v", job, err)
	}
	close(installer.wait)
	time.Sleep(30 * time.Millisecond)
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&count)
	if count != 0 || events.Load() != 1 {
		t.Fatalf("servers=%d events=%d", count, events.Load())
	}
}

func TestConcurrentSameRootRejectedAndBootstrapDifferentRootsAllowed(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	installer := &fakeInstaller{wait: make(chan struct{}), started: make(chan struct{}, 2), createExecutable: true}
	service := NewWithOptions(db, &templateSource{template: template}, installer, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
	defer service.Close()
	first, err := service.Start(context.Background(), Request{TemplateID: "template", ServerName: "One", DirectoryName: "same", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), Request{TemplateID: "template", ServerName: "Two", DirectoryName: "same", ActorUserID: "actor"}); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("same-root error=%v", err)
	}
	second, err := service.Start(context.Background(), Request{TemplateID: "template", ServerName: "Two", DirectoryName: "different", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	close(installer.wait)
	if waitTerminal(t, service, first.ID).Status != Completed || waitTerminal(t, service, second.ID).Status != Completed {
		t.Fatal("parallel roots did not complete")
	}
}

func TestExistingTargetAndMissingTemplate(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	os.MkdirAll(filepath.Join(data, "servers", "seven"), 0700)
	os.WriteFile(filepath.Join(data, "servers", "seven", "foreign.txt"), []byte("x"), 0600)
	service := NewWithOptions(db, &templateSource{template: template}, &fakeInstaller{}, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	if _, err := service.Start(context.Background(), Request{TemplateID: "template", ServerName: "Seven", DirectoryName: "seven", ActorUserID: "actor"}); !errors.Is(err, ErrTargetConflict) {
		t.Fatalf("target error=%v", err)
	}
	service.templates = &templateSource{err: sql.ErrNoRows}
	if _, err := service.Start(context.Background(), Request{TemplateID: "missing", ServerName: "Seven", DirectoryName: "other", ActorUserID: "actor"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing error=%v", err)
	}
}

func TestRestartMarksActiveJobsInterrupted(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	store := NewStore(db)
	now := time.Now().UTC()
	for _, fixture := range []struct {
		phase       string
		installed   bool
		snapshot    bool
		recoverable bool
	}{
		{Installing, false, false, false},
		{ValidatingInstallation, true, false, false},
		{RegisteringServer, true, true, true},
		{ServerRegistered, true, true, true},
	} {
		job := Job{ID: "job-" + fixture.phase, ActorUserID: "actor", TemplateID: "template", TemplateName: template.Name, ServerName: "Seven", DirectoryName: "seven-" + fixture.phase, InstallerType: "steamcmd", AppID: 294420, Status: fixture.phase, CurrentPhase: fixture.phase, Summary: "Active", InstallationCompleted: fixture.installed, CreatedAt: now, UpdatedAt: now}
		if err := store.Create(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		if fixture.snapshot {
			if err := store.SaveRegistrationSnapshot(context.Background(), job.ID, []byte(`{"server":{"id":"server"},"template_id":"template"}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	service := NewWithOptions(db, &templateSource{template: template}, &fakeInstaller{}, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	if err := service.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		phase       string
		recoverable bool
	}{{Installing, false}, {ValidatingInstallation, false}, {RegisteringServer, true}, {ServerRegistered, true}} {
		job, err := service.Get(context.Background(), "job-"+fixture.phase)
		if err != nil || job.Status != Failed || job.CurrentPhase != Failed || !job.FilesMayRemain || job.RegistrationRecoverable != fixture.recoverable {
			t.Fatalf("phase=%s job=%#v err=%v", fixture.phase, job, err)
		}
	}
}

func TestProvisioningEventHistoryIsBoundedAndOrdered(t *testing.T) {
	db, _, _ := provisionFixture(t)
	defer db.Close()
	store := NewStore(db)
	now := time.Now().UTC()
	job := Job{ID: "events", ActorUserID: "actor", TemplateID: "template", TemplateName: "Template", ServerName: "Server", DirectoryName: "events", InstallerType: "steamcmd", Status: Pending, CurrentPhase: Pending, Summary: "Queued", CreatedAt: now, UpdatedAt: now}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 250; index++ {
		store.Event(context.Background(), job.ID, Installing, "EVENT", strconv.Itoa(index), now.Add(time.Duration(index)*time.Millisecond))
	}
	events, err := store.Events(context.Background(), job.ID)
	if err != nil || len(events) != 200 || events[0].Summary != "0" || events[199].Summary != "199" {
		t.Fatalf("events=%d first=%#v last=%#v err=%v", len(events), events[0], events[len(events)-1], err)
	}
}

func TestPlatformProvisionability(t *testing.T) {
	_, _, template := provisionFixture(t)
	values, _, _ := templates.ResolveValues(template, nil)
	if _, err := CheckProvisionable(template, values, "linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckProvisionable(template, values, "windows"); !errors.Is(err, ErrNotProvisionable) {
		t.Fatalf("Windows accepted Linux launch: %v", err)
	}
}

func TestSafeWindowsExecutableIsProvisionable(t *testing.T) {
	_, _, template := provisionFixture(t)
	template.Installer.SteamCMD.Platform = "windows"
	template.Launch.Executable = "DedicatedServer.exe"
	values, _, _ := templates.ResolveValues(template, nil)
	if _, err := CheckProvisionable(template, values, "windows"); err != nil {
		t.Fatalf("safe Windows launch rejected: %v", err)
	}
	if _, err := CheckProvisionable(template, values, "linux"); !errors.Is(err, ErrNotProvisionable) {
		t.Fatalf("Windows-only launch accepted on Linux: %v", err)
	}
}

func TestProvisionabilityPreviewAllowsRequiredUserConfiguration(t *testing.T) {
	db, data, template := provisionFixture(t)
	defer db.Close()
	template.Variables[1].DefaultValue = ""
	service := NewWithOptions(db, &templateSource{template: template}, &fakeInstaller{}, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	result, err := service.Check(context.Background(), template.ID)
	if err != nil || !result.Provisionable {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestSensitiveValueCannotBecomeExecutablePath(t *testing.T) {
	db, root, template := provisionFixture(t)
	defer db.Close()
	template.Launch.Executable = "./${BETA_PASSWORD}"
	if _, err := templates.ResolveLaunch(template, "linux", map[string]string{"BETA_PASSWORD": "secret"}, root); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe or leaking error: %v", err)
	}
}

func officialSteamProvisionFixture(t *testing.T, hostOS string) (*sql.DB, string, templates.Template) {
	t.Helper()
	db, data, template := provisionFixture(t)
	minimum, maximum := float64(1024), float64(65535)
	template.SourceType = templates.SourceOfficial
	template.Version = "1.0.0"
	template.Platforms = []string{"windows", "linux"}
	template.Launch = nil
	template.PlatformLaunches = map[string]templates.LaunchDefinition{
		"windows": {Executable: "Server.exe", Arguments: []string{"-port={{SERVER_PORT}}", "-name={{SERVER_NAME}}"}, WorkingRoot: "server_root", StopMethod: "terminate", StopTimeout: 30},
		"linux":   {Executable: "./Server.x86_64", Arguments: []string{"-port={{SERVER_PORT}}", "-name={{SERVER_NAME}}"}, WorkingRoot: "server_root", StopMethod: "stdin_command", StopCommand: "shutdown", StopTimeout: 20},
	}
	template.Variables = []templates.TemplateVariable{{Name: "Port", Key: "SERVER_PORT", DefaultValue: "26900", UserEditable: true, Type: "integer", Required: true, Validation: templates.Validation{Min: &minimum, Max: &maximum}}, {Name: "Name", Key: "SERVER_NAME", DefaultValue: "Seven", UserEditable: true, Type: "string", Required: true}}
	template.Ports = []templates.TemplatePort{{Name: "Game TCP", Protocol: "tcp", Variable: "SERVER_PORT"}, {Name: "Game UDP", Protocol: "udp", Variable: "SERVER_PORT"}}
	template.Installer.SteamCMD.BetaBranchVariable = ""
	template.Installer.SteamCMD.BetaPasswordVariable = ""
	template.Installer.SteamCMD.Platform = "native"
	_ = hostOS
	return db, data, template
}

func TestOfficialSteamProvisioningSelectsPlatformCreatesPortsAndProvenance(t *testing.T) {
	for _, test := range []struct{ host, executable string }{{"windows", "Server.exe"}, {"linux", "Server.x86_64"}} {
		t.Run(test.host, func(t *testing.T) {
			db, data, template := officialSteamProvisionFixture(t, test.host)
			defer db.Close()
			template.ResolvedAdapters = []templates.ConfigAdapterDefinition{{SchemaVersion: 1, ID: "serverconfig", Version: "1.0.0", Format: "xml-properties", Target: "serverconfig.xml", RestartRequired: true, Fields: []templates.ConfigAdapterField{{Key: "SERVER_PORT", Label: "Port", Type: "integer", Property: "ServerPort", Required: true, Validation: template.Variables[0].Validation}}}}
			installer := &fakeInstaller{createExecutable: true, executable: test.executable, configXML: `<ServerSettings><property name="ServerPort" value="26900"/></ServerSettings>`}
			serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
			service := NewWithOptions(db, &templateSource{template: template}, installer, serverService, data, Options{HostOS: test.host})
			defer service.Close()
			preview, err := service.Check(context.Background(), template.ID)
			if err != nil || !preview.Provisionable || preview.AppID != 294420 || preview.LaunchExecutable == "" {
				t.Fatalf("preview=%#v err=%v", preview, err)
			}
			job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Official", DirectoryName: "official-" + test.host, Values: map[string]string{"SERVER_PORT": "26910", "SERVER_NAME": "Official Test"}, ActorUserID: "actor"})
			if err != nil {
				t.Fatal(err)
			}
			job = waitTerminal(t, service, job.ID)
			if job.Status != Completed || job.ServerID == "" {
				t.Fatalf("job=%#v", job)
			}
			record, err := serverService.Get(context.Background(), job.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(record.Server.Executable) != test.executable || record.Server.Arguments[0] != "-port=26910" || record.Server.Arguments[1] != "-name=Official Test" {
				t.Fatalf("server=%#v", record.Server)
			}
			if test.host == "linux" && (record.Server.StopMethod != "stdin_command" || record.Server.StopCommand != "shutdown" || record.Server.StopTimeoutSeconds != 20) {
				t.Fatalf("stop=%#v", record.Server)
			}
			var portCount int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=? AND port=26910`, job.ServerID).Scan(&portCount); err != nil || portCount != 2 {
				t.Fatalf("ports=%d err=%v", portCount, err)
			}
			var source, version string
			if err = db.QueryRow(`SELECT template_source,template_version FROM server_template_variables WHERE server_id=? LIMIT 1`, job.ServerID).Scan(&source, &version); err != nil || source != templates.SourceOfficial || version != "1.0.0" {
				t.Fatalf("provenance=%s/%s err=%v", source, version, err)
			}
			var adapterCount int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_adapters WHERE server_id=? AND adapter_id='serverconfig' AND adapter_version='1.0.0'`, job.ServerID).Scan(&adapterCount); err != nil || adapterCount != 1 {
				t.Fatalf("config snapshots=%d err=%v", adapterCount, err)
			}
			configData, readErr := os.ReadFile(filepath.Join(data, "servers", "official-"+test.host, "serverconfig.xml"))
			if readErr != nil || !strings.Contains(string(configData), `value="26910"`) {
				t.Fatalf("config=%s err=%v", configData, readErr)
			}
		})
	}
}

func TestGameConfigFailureHasDedicatedPhaseCodeAndNoServerRecord(t *testing.T) {
	db, data, template := officialSteamProvisionFixture(t, "windows")
	defer db.Close()
	template.ResolvedAdapters = []templates.ConfigAdapterDefinition{{SchemaVersion: 1, ID: "serverconfig", Version: "1.0.0", Format: "xml-properties", Target: "serverconfig.xml", RestartRequired: true, Fields: []templates.ConfigAdapterField{{Key: "SERVER_PORT", Label: "Port", Type: "integer", Property: "ServerPort", Required: true, Validation: template.Variables[0].Validation}}}}
	installer := &fakeInstaller{createExecutable: true, executable: "Server.exe", configXML: `<ServerSettings><property name="ServerPort" value="26900"></ServerSettings>`}
	service := NewWithOptions(db, &templateSource{template: template}, installer, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "windows"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Broken Config", DirectoryName: "broken-config", Values: map[string]string{"SERVER_PORT": "26910", "SERVER_NAME": "Broken"}, ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	var serverCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&serverCount)
	if job.Status != Failed || !job.InstallationCompleted || job.FailurePhase != ResolvingLaunch || job.FailureCode != "GAME_CONFIG_PARSE_FAILED" || job.RegistrationRecoverable || serverCount != 0 {
		t.Fatalf("job=%#v server_count=%d", job, serverCount)
	}
}

func TestOfficialSteamProvisioningPersistsPostStartAdapterWithoutInventingConfig(t *testing.T) {
	db, data, template := officialSteamProvisionFixture(t, "windows")
	defer db.Close()
	maxLength := 64
	template.ResolvedAdapters = []templates.ConfigAdapterDefinition{{SchemaVersion: 1, ID: "project-zomboid-server-ini", Version: "1.0.0", Format: "ini-key-values", Target: "Server/gamenode.ini", RestartRequired: true, PostStartOnly: true, Fields: []templates.ConfigAdapterField{{Key: "PZ_PUBLIC_NAME", Label: "Public name", Type: "string", Property: "PublicName", Required: true, Validation: templates.Validation{MaxLength: &maxLength}}}}}
	installer := &fakeInstaller{createExecutable: true, executable: "Server.exe"}
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	service := NewWithOptions(db, &templateSource{template: template}, installer, serverService, data, Options{HostOS: "windows"})
	defer service.Close()
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Post Start", DirectoryName: "post-start", Values: map[string]string{"SERVER_PORT": "26910", "SERVER_NAME": "Post Start"}, ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.Status != Completed || job.ServerID == "" {
		t.Fatalf("job=%#v", job)
	}
	if _, err = os.Stat(filepath.Join(data, "servers", "post-start", "Server", "gamenode.ini")); !os.IsNotExist(err) {
		t.Fatalf("post-start configuration was invented during provisioning: %v", err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_adapters WHERE server_id=? AND adapter_id='project-zomboid-server-ini'`, job.ServerID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("adapter snapshot count=%d err=%v", count, err)
	}
}

func TestPalworldOfficialProvisioningSeedsAndAppliesEveryManagedValue(t *testing.T) {
	db, data, _ := provisionFixture(t)
	defer db.Close()
	templateData, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "palworld", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var template templates.Template
	if err = json.Unmarshal(templateData, &template); err != nil {
		t.Fatal(err)
	}
	adapterData, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "palworld", "palworld-settings.adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var adapter templates.ConfigAdapterDefinition
	if err = json.Unmarshal(adapterData, &adapter); err != nil {
		t.Fatal(err)
	}
	template.ResolvedAdapters = []templates.ConfigAdapterDefinition{adapter}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "palworld", "fixtures", "PalWorldSettings.example.ini"))
	if err != nil {
		t.Fatal(err)
	}
	installer := &fakeInstaller{createExecutable: true, executable: "PalServer.exe", files: map[string][]byte{"DefaultPalWorldSettings.ini": fixture, "Pal/.keep": []byte("fixture")}}
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	service := NewWithOptions(db, &templateSource{template: template}, installer, serverService, data, Options{HostOS: "windows"})
	defer service.Close()
	values := map[string]string{
		"SERVER_PORT": "18211", "MAX_PLAYERS": "24", "LOG_FORMAT": "Json", "SERVER_NAME": "Release Integration",
		"SERVER_DESCRIPTION": `Comma, quote " and slash \`, "SERVER_PASSWORD": "player-secret", "ADMIN_PASSWORD": "admin-secret",
		"RCON_ENABLED": "false", "RCON_PORT": "25576", "REST_API_ENABLED": "false", "REST_API_PORT": "18212", "BACKUP_ENABLED": "true",
	}
	job, err := service.Start(context.Background(), Request{TemplateID: "palworld", ServerName: "Palworld", DirectoryName: "palworld-integration", Values: values, ActorUserID: "actor", ActorUsername: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.Status != Completed || !job.InstallationCompleted || job.ServerID == "" {
		t.Fatalf("job=%#v", job)
	}
	root := filepath.Join(data, "servers", "palworld-integration")
	configured, err := gameconfig.Read(root, adapter)
	if err != nil {
		t.Fatal(err)
	}
	for key, expected := range values {
		if _, managed := configured[key]; managed && configured[key] != expected {
			t.Fatalf("%s=%q want %q", key, configured[key], expected)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(adapter.Target)))
	if err != nil || !strings.Contains(string(raw), "UnknownFutureSetting=-12") || !strings.Contains(string(raw), "CrossplayPlatforms=(Steam,Xbox,PS5,Mac)") {
		t.Fatalf("unknown defaults were not preserved: %v %s", err, raw)
	}
	record, err := serverService.Get(context.Background(), job.ServerID)
	var portCount int
	portErr := db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=?`, job.ServerID).Scan(&portCount)
	if err != nil || filepath.Base(record.Server.Executable) != "PalServer.exe" || record.Server.Arguments[0] != "-port=18211" || portErr != nil || portCount != 3 {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	encoded, _ := json.Marshal(job)
	if strings.Contains(string(encoded), "player-secret") || strings.Contains(string(encoded), "admin-secret") {
		t.Fatal("Palworld secret leaked through provisioning job")
	}
}

func TestSatisfactoryOfficialProvisioningUsesDirectPlatformLaunches(t *testing.T) {
	for _, test := range []struct {
		host, executable  string
		wantFirstArgument string
	}{
		{host: "windows", executable: "FactoryServer.exe", wantFirstArgument: "-log"},
	} {
		t.Run(test.host, func(t *testing.T) {
			db, data, _ := provisionFixture(t)
			defer db.Close()
			templateData, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "satisfactory", "template.json"))
			if err != nil {
				t.Fatal(err)
			}
			var template templates.Template
			if err = json.Unmarshal(templateData, &template); err != nil {
				t.Fatal(err)
			}
			installer := &fakeInstaller{createExecutable: true, executable: test.executable, files: map[string][]byte{"Engine/.fixture": []byte("engine"), "FactoryGame/.fixture": []byte("game")}}
			serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
			service := NewWithOptions(db, &templateSource{template: template}, installer, serverService, data, Options{HostOS: test.host})
			defer service.Close()
			values := map[string]string{"SERVER_PORT": "37777", "RELIABLE_PORT": "38888", "MAX_PLAYERS": "8", "RELEASE_BRANCH": "experimental"}
			job, err := service.Start(context.Background(), Request{TemplateID: "satisfactory", ServerName: "Satisfactory", DirectoryName: "satisfactory-" + test.host, Values: values, ActorUserID: "actor"})
			if err != nil {
				t.Fatal(err)
			}
			job = waitTerminal(t, service, job.ID)
			if job.Status != Completed || job.ServerID == "" || !job.InstallationCompleted {
				t.Fatalf("job=%#v", job)
			}
			record, err := serverService.Get(context.Background(), job.ServerID)
			if err != nil || filepath.Base(record.Server.Executable) != filepath.Base(test.executable) || record.Server.Arguments[0] != test.wantFirstArgument || installer.plan.AppID != 1690800 || installer.plan.BetaBranch != "experimental" || record.Server.StopMethod != "console_interrupt" || record.Server.StopCommand != "" {
				t.Fatalf("record=%#v plan=%#v err=%v", record, installer.plan, err)
			}
			joined := strings.Join(record.Server.Arguments, " ")
			for _, expected := range []string{"-Port=37777", "-ReliablePort=38888", "MaxPlayers=8"} {
				if !strings.Contains(joined, expected) {
					t.Fatalf("missing launch argument %q in %#v", expected, record.Server.Arguments)
				}
			}
			var portCount int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=? AND port IN (37777,38888)`, job.ServerID).Scan(&portCount); err != nil || portCount != 3 {
				t.Fatalf("ports=%d err=%v", portCount, err)
			}
			var adapterCount int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_adapters WHERE server_id=?`, job.ServerID).Scan(&adapterCount); err != nil || adapterCount != 0 {
				t.Fatalf("unexpected config adapter count=%d err=%v", adapterCount, err)
			}
		})
	}
}

func TestSatisfactoryOfficialTemplateRejectsLinuxProvisioning(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "satisfactory", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var template templates.Template
	if err = json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	values, _, err := templates.ResolveValues(template, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = CheckProvisionable(template, values, "linux"); !errors.Is(err, ErrNotProvisionable) {
		t.Fatalf("Linux Satisfactory provisioning unexpectedly available: %v", err)
	}
}

func TestOfficialSteamProvisioningRejectsUnsupportedPlatformAndMissingExecutable(t *testing.T) {
	db, data, template := officialSteamProvisionFixture(t, "linux")
	defer db.Close()
	delete(template.PlatformLaunches, "linux")
	service := NewWithOptions(db, &templateSource{template: template}, &fakeInstaller{}, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
	defer service.Close()
	if _, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "No Linux", DirectoryName: "no-linux", ActorUserID: "actor"}); !errors.Is(err, ErrNotProvisionable) {
		t.Fatalf("unsupported platform=%v", err)
	}
	template.PlatformLaunches["linux"] = templates.LaunchDefinition{Executable: "missing.x86_64", WorkingRoot: "server_root", StopMethod: "terminate"}
	service.templates = &templateSource{template: template}
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Missing", DirectoryName: "missing-exe", ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.Status != Failed {
		t.Fatalf("job=%#v", job)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&count)
	if count != 0 {
		t.Fatal("missing executable created server record")
	}
}

func TestOfficialSteamProvisioningRejectsUnavailableRequiredAdapter(t *testing.T) {
	_, _, template := officialSteamProvisionFixture(t, "linux")
	template.Configuration = &templates.ConfigurationDefinition{Adapters: []templates.ConfigAdapterReference{{ID: "missing", SchemaVersion: 1, File: "missing.adapter.json"}}}
	values, _, err := templates.ResolveValues(template, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = CheckProvisionable(template, values, "linux"); !errors.Is(err, ErrNotProvisionable) {
		t.Fatalf("missing adapter accepted: %v", err)
	}
}

func waitTerminal(t *testing.T, service *Service, id string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := service.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == Completed || job.Status == Failed || job.Status == Cancelled {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}
