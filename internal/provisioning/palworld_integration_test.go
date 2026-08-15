package provisioning_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gamenode "gamenode"
	"gamenode/internal/database"
	"gamenode/internal/gameconfig"
	"gamenode/internal/provisioning"
	gameruntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
)

// TestPalworldFullDeploymentIntegration is a manual, networked Windows
// acceptance. Normal tests never download or launch Palworld.
func TestPalworldFullDeploymentIntegration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Palworld official template advertises Windows only")
	}
	data := os.Getenv("GAMENODE_PALWORLD_FULL_ACCEPTANCE_DATA")
	if data == "" {
		t.Skip("set GAMENODE_PALWORLD_FULL_ACCEPTANCE_DATA to an empty isolated GameNode data directory")
	}
	data, err := filepath.Abs(data)
	if err != nil {
		t.Fatal(err)
	}
	if entries, readErr := os.ReadDir(data); readErr == nil && len(entries) != 0 {
		t.Fatal("Palworld acceptance requires an empty isolated data directory")
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
	palworld, ok := catalog.Get("palworld")
	if !ok || len(palworld.ResolvedAdapters) != 1 {
		t.Fatalf("Palworld catalog adapter unavailable: ok=%v adapters=%d", ok, len(palworld.ResolvedAdapters))
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
	provisioner.SetObserver(func(event provisioning.Event) {
		t.Logf("provisioning %s: %s", event.Job.Status, event.Job.Summary)
	})
	values := map[string]string{
		"SERVER_PORT": "38211", "MAX_PLAYERS": "8", "LOG_FORMAT": "text",
		"SERVER_NAME": "GameNode Palworld Acceptance", "SERVER_DESCRIPTION": "Real GameNode adapter acceptance",
		"SERVER_PASSWORD": "palworld-acceptance-player-secret", "ADMIN_PASSWORD": "palworld-acceptance-admin-secret",
		"RCON_ENABLED": "false", "RCON_PORT": "38212", "REST_API_ENABLED": "false", "REST_API_PORT": "38213", "BACKUP_ENABLED": "true",
	}
	job, err := provisioner.Start(context.Background(), provisioning.Request{TemplateID: "palworld", ServerName: "Palworld Acceptance", DirectoryName: "palworld", Values: values, ActorUserID: "acceptance-user", ActorUsername: "acceptance"})
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
	if job.Status != provisioning.Completed || !job.InstallationCompleted || job.ServerID == "" {
		t.Fatalf("provisioning failed: status=%s phase=%s code=%s summary=%s error=%s", job.Status, job.FailurePhase, job.FailureCode, job.Summary, job.ErrorSummary)
	}
	jobJSON, _ := json.Marshal(job)
	for _, secret := range []string{values["SERVER_PASSWORD"], values["ADMIN_PASSWORD"]} {
		if strings.Contains(string(jobJSON), secret) {
			t.Fatal("Palworld secret leaked into provisioning job or event data")
		}
	}
	root := filepath.Join(data, "servers", "palworld")
	for _, required := range []string{"PalServer.exe", "DefaultPalWorldSettings.ini", "Pal/Saved/Config/WindowsServer/PalWorldSettings.ini"} {
		if _, err = os.Stat(filepath.Join(root, filepath.FromSlash(required))); err != nil {
			t.Fatalf("required real artifact %s: %v", required, err)
		}
	}
	adapter := palworld.ResolvedAdapters[0]
	configured, err := gameconfig.Read(root, adapter)
	if err != nil {
		t.Fatal(err)
	}
	for key, expected := range values {
		if actual, managed := configured[key]; managed && actual != expected {
			t.Fatalf("managed value %s was not applied", key)
		}
	}
	target := filepath.Join(root, filepath.FromSlash(adapter.Target))
	configData, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(configData), "Difficulty=") {
		t.Fatalf("real upstream defaults were not preserved: %v", err)
	}
	configService := gameconfig.New(db, serverService)
	updated, err := configService.Update(context.Background(), job.ServerID, adapter.ID, map[string]string{"SERVER_NAME": "GameNode Palworld Edited"})
	if err != nil || len(updated.Adapters) != 1 || !updated.Adapters[0].RestartRequired {
		t.Fatalf("post-provision config edit failed: %#v %v", updated, err)
	}
	configured, err = gameconfig.Read(root, adapter)
	if err != nil || configured["SERVER_NAME"] != "GameNode Palworld Edited" {
		t.Fatalf("post-provision config edit not persisted: %v", err)
	}
	for _, field := range updated.Adapters[0].Fields {
		if field.Sensitive && field.Value != "" {
			t.Fatal("managed configuration API returned a sensitive value")
		}
	}
	var portCount, adapterCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=? AND port IN (38211,38212,38213)`, job.ServerID).Scan(&portCount); err != nil || portCount != 3 {
		t.Fatalf("registered ports=%d err=%v", portCount, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_adapters WHERE server_id=? AND adapter_id='palworld-settings'`, job.ServerID).Scan(&adapterCount); err != nil || adapterCount != 1 {
		t.Fatalf("adapter snapshots=%d err=%v", adapterCount, err)
	}
	record, err := serverService.Get(context.Background(), job.ServerID)
	if err != nil || filepath.Base(record.Server.Executable) != "PalServer.exe" || record.Server.StopMethod != "terminate" {
		t.Fatalf("unexpected registered server: %#v err=%v", record, err)
	}

	startAndObserve := func(duration time.Duration) {
		t.Helper()
		record, startErr := serverService.Start(context.Background(), job.ServerID)
		if startErr != nil {
			t.Fatal(startErr)
		}
		t.Logf("PalServer started: pid=%d", record.Runtime.PID)
		deadline := time.Now().Add(duration)
		for time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			current, getErr := serverService.Get(context.Background(), job.ServerID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if current.Runtime.CurrentState == servers.StateCrashed || current.Runtime.CurrentState == servers.StateStopped {
				t.Fatalf("PalServer exited during bounded startup observation: state=%s error=%s", current.Runtime.CurrentState, current.Runtime.LastError)
			}
		}
	}
	defer func() {
		current, getErr := serverService.Get(context.Background(), job.ServerID)
		if getErr == nil && (current.Runtime.CurrentState == servers.StateRunning || current.Runtime.CurrentState == servers.StateStarting || current.Runtime.CurrentState == servers.StateStopping) {
			_, _ = serverService.Kill(context.Background(), job.ServerID)
		}
	}()
	startAndObserve(90 * time.Second)
	stopStarted := time.Now()
	stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	record, err = serverService.Stop(stopCtx, job.ServerID)
	cancel()
	if err != nil || record.Runtime.CurrentState != servers.StateStopped {
		t.Fatalf("Palworld terminate stop failed: state=%s err=%v", record.Runtime.CurrentState, err)
	}
	t.Logf("Palworld terminate lifecycle completed in %s; this does not prove an application-level graceful save", time.Since(stopStarted).Round(time.Second))
	startAndObserve(30 * time.Second)
	stopCtx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
	_, err = serverService.Stop(stopCtx, job.ServerID)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	configured, err = gameconfig.Read(root, adapter)
	if err != nil || configured["SERVER_NAME"] != "GameNode Palworld Edited" {
		t.Fatalf("configuration was not retained across restart: %v", err)
	}
}
