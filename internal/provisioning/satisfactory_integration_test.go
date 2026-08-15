package provisioning_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gamenode "gamenode"
	"gamenode/internal/database"
	"gamenode/internal/provisioning"
	gameruntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
)

// TestSatisfactoryFullDeploymentIntegration is a manual networked Windows
// acceptance. Normal test runs never download or launch Satisfactory.
func TestSatisfactoryFullDeploymentIntegration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the initial Satisfactory official template advertises Windows only")
	}
	data := os.Getenv("GAMENODE_SATISFACTORY_FULL_ACCEPTANCE_DATA")
	if data == "" {
		t.Skip("set GAMENODE_SATISFACTORY_FULL_ACCEPTANCE_DATA to an empty isolated GameNode data directory")
	}
	data, err := filepath.Abs(data)
	if err != nil {
		t.Fatal(err)
	}
	if entries, readErr := os.ReadDir(data); readErr == nil && len(entries) != 0 {
		t.Fatal("Satisfactory acceptance requires an empty isolated data directory")
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	if err = os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(data, "gamenode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatal(err)
	}
	catalog := templates.NewCatalogManager(repositoryCatalogSource{root: repositoryRoot}, data, "0.2.0")
	result, err := catalog.Refresh(context.Background())
	if err != nil || result.Status.Source != "remote" {
		t.Fatalf("catalog refresh: source=%q err=%v", result.Status.Source, err)
	}
	templateService := templates.NewServiceWithCatalog(templates.NewStore(db), catalog)
	platform, err := steamcmd.CurrentPlatform(runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	steamManager := steamcmd.New(filepath.Join(data, "tools", "steamcmd"), platform, nil, nil)
	serverService := servers.NewService(servers.NewStore(db), gameruntime.NewNative())
	provisioner := provisioning.New(db, templateService, steamManager, serverService, data)
	defer provisioner.Close()
	if err = provisioner.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := provisioner.Start(context.Background(), provisioning.Request{TemplateID: "satisfactory", ServerName: "Satisfactory Acceptance", DirectoryName: "satisfactory", Values: map[string]string{"SERVER_PORT": "37777", "RELIABLE_PORT": "38888", "MAX_PLAYERS": "4", "RELEASE_BRANCH": "public"}, ActorUserID: "acceptance-user", ActorUsername: "acceptance"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Minute)
	for time.Now().Before(deadline) {
		job, err = provisioner.Get(context.Background(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == provisioning.Completed || job.Status == provisioning.Failed || job.Status == provisioning.Cancelled {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if job.Status != provisioning.Completed || job.ServerID == "" || !job.InstallationCompleted {
		t.Fatalf("provisioning failed: status=%s phase=%s code=%s summary=%s error=%s", job.Status, job.FailurePhase, job.FailureCode, job.Summary, job.ErrorSummary)
	}
	record, err := serverService.Get(context.Background(), job.ServerID)
	if err != nil || filepath.Base(record.Server.Executable) != "FactoryServer.exe" || record.Server.StopMethod != "terminate" {
		t.Fatalf("unexpected server: %#v err=%v", record, err)
	}
	joined := strings.Join(record.Server.Arguments, " ")
	for _, expected := range []string{"-Port=37777", "-ReliablePort=38888", "MaxPlayers=4"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing argument %q in %#v", expected, record.Server.Arguments)
		}
	}
	var portCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=? AND port IN (37777,38888)`, job.ServerID).Scan(&portCount); err != nil || portCount != 3 {
		t.Fatalf("ports=%d err=%v", portCount, err)
	}
	observe := func(duration time.Duration) {
		t.Helper()
		started, startErr := serverService.Start(context.Background(), job.ServerID)
		if startErr != nil {
			t.Fatal(startErr)
		}
		t.Logf("FactoryServer started: pid=%d", started.Runtime.PID)
		deadline := time.Now().Add(duration)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			current, getErr := serverService.Get(context.Background(), job.ServerID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if current.Runtime.CurrentState == servers.StateStopped || current.Runtime.CurrentState == servers.StateCrashed {
				t.Fatalf("Satisfactory exited during startup observation: state=%s error=%s", current.Runtime.CurrentState, current.Runtime.LastError)
			}
		}
	}
	defer func() {
		current, getErr := serverService.Get(context.Background(), job.ServerID)
		if getErr == nil && (current.Runtime.CurrentState == servers.StateRunning || current.Runtime.CurrentState == servers.StateStarting || current.Runtime.CurrentState == servers.StateStopping) {
			_, _ = serverService.Kill(context.Background(), job.ServerID)
		}
	}()
	observe(90 * time.Second)
	stopCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	_, err = serverService.Stop(stopCtx, job.ServerID)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	observe(30 * time.Second)
	stopCtx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	_, err = serverService.Stop(stopCtx, job.ServerID)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Windows terminate/start/stop/restart completed; this does not prove an application-level graceful save")
}
