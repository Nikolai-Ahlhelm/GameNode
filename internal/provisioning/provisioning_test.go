package provisioning

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/database"
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
}

func (i *fakeInstaller) Install(ctx context.Context, root string, plan steamcmd.InstallPlan, _ io.Writer, sink steamcmd.EventSink) error {
	i.calls.Add(1)
	i.plan = plan
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
		return os.WriteFile(filepath.Join(root, "server.x86_64"), []byte("test"), 0700)
	}
	return nil
}

type failingCreator struct{ calls atomic.Int32 }

func (c *failingCreator) CreateProvisioned(context.Context, servers.Server, string, []servers.ProvisionedVariable) (servers.Record, error) {
	c.calls.Add(1)
	return servers.Record{}, errors.New("database unavailable SECRET")
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
	record, err := serverService.Get(context.Background(), job.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Server.CreationMode != servers.CreationTemplate || record.Server.Arguments[0] != "-port=26901" || record.Server.Arguments[1] != "-name=My Server" {
		t.Fatalf("server=%#v", record.Server)
	}
	if installer.plan.AppID != 294420 || installer.plan.BetaBranch != "latest_experimental" {
		t.Fatalf("plan=%#v", installer.plan)
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
	job := Job{ID: "job", ActorUserID: "actor", TemplateID: "template", TemplateName: template.Name, ServerName: "Seven", DirectoryName: "seven", InstallerType: "steamcmd", AppID: 294420, Status: Installing, Summary: "Installing", CreatedAt: now, UpdatedAt: now}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	service := NewWithOptions(db, &templateSource{template: template}, &fakeInstaller{}, &failingCreator{}, data, Options{HostOS: "linux"})
	defer service.Close()
	if err := service.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := service.Get(context.Background(), "job")
	if err != nil || job.Status != Failed || !job.FilesMayRemain {
		t.Fatalf("job=%#v err=%v", job, err)
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
	if _, _, err := buildServer(template, "Seven", root, map[string]string{"BETA_PASSWORD": "secret"}, map[string]bool{"BETA_PASSWORD": true}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe or leaking error: %v", err)
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
