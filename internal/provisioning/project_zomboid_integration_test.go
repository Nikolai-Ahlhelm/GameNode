package provisioning_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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

type repositoryCatalogSource struct{ root string }

func (s repositoryCatalogSource) FetchCatalog(_ context.Context) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.root, "catalog.json"))
}

func (s repositoryCatalogSource) FetchTemplate(_ context.Context, name string) ([]byte, error) {
	target := filepath.Join(s.root, filepath.FromSlash(name))
	relative, err := filepath.Rel(s.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("template path escapes repository catalog")
	}
	return os.ReadFile(target)
}

// TestProjectZomboidFullDeploymentIntegration is a manual real-game
// acceptance. It exercises the repository catalog, production provisioning
// service, managed SteamCMD, server persistence, native runtime, console, and
// graceful stdin stop. The regular test suite never reaches the network.
func TestProjectZomboidFullDeploymentIntegration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Project Zomboid v1 template is acceptance-tested on Windows only")
	}
	data := os.Getenv("GAMENODE_PZ_FULL_ACCEPTANCE_DATA")
	if data == "" {
		t.Skip("set GAMENODE_PZ_FULL_ACCEPTANCE_DATA to an empty isolated GameNode data directory")
	}
	data, err := filepath.Abs(data)
	if err != nil {
		t.Fatal(err)
	}
	reuseProvisioned := false
	if entries, readErr := os.ReadDir(data); readErr == nil && len(entries) != 0 {
		manifest := filepath.Join(data, "servers", "project-zomboid", "steamapps", "appmanifest_380870.acf")
		if _, dbErr := os.Stat(filepath.Join(data, "gamenode.db")); dbErr != nil {
			t.Fatal("non-empty acceptance directory does not contain the isolated GameNode database")
		}
		manifestData, manifestErr := os.ReadFile(manifest)
		if manifestErr != nil || !strings.Contains(string(manifestData), `"appid"`+"\t\t"+`"380870"`) {
			t.Fatal("non-empty acceptance directory is not a completed Project Zomboid installation")
		}
		reuseProvisioned = true
	} else if readErr != nil && !os.IsNotExist(readErr) {
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
	if err != nil {
		t.Fatal(err)
	}
	if result.Status.Source != "remote" {
		t.Fatalf("catalog source = %q", result.Status.Source)
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

	serverID := ""
	if reuseProvisioned {
		if err = db.QueryRow(`SELECT server_id FROM provisioning_jobs WHERE template_id='project-zomboid' AND status='completed' ORDER BY created_at DESC LIMIT 1`).Scan(&serverID); err != nil {
			t.Fatal(err)
		}
		t.Log("reusing the completed isolated GameNode provisioning result")
	} else {
		job, startErr := provisioner.Start(context.Background(), provisioning.Request{
			TemplateID:    "project-zomboid",
			ServerName:    "Project Zomboid Acceptance",
			DirectoryName: "project-zomboid",
			Values:        map[string]string{"SERVER_PORT": "16261"},
			ActorUserID:   "acceptance-user",
			ActorUsername: "acceptance",
		})
		if startErr != nil {
			t.Fatal(startErr)
		}
		deadline := time.Now().Add(45 * time.Minute)
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
		if job.Status != provisioning.Completed || job.ServerID == "" {
			t.Fatalf("provisioning did not complete: status=%s summary=%s error=%s", job.Status, job.Summary, job.ErrorSummary)
		}
		serverID = job.ServerID
	}
	if err = serverService.Rediscover(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, err := serverService.Get(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Server.CreationMode != servers.CreationTemplate || !strings.HasSuffix(strings.ToLower(record.Server.Executable), `jre64\bin\java.exe`) {
		t.Fatalf("unexpected provisioned launch: mode=%s executable=%s", record.Server.CreationMode, record.Server.Executable)
	}
	var source, version string
	if err = db.QueryRow(`SELECT template_source,template_version FROM server_template_variables WHERE server_id=? AND variable_key='SERVER_PORT'`, serverID).Scan(&source, &version); err != nil {
		t.Fatal(err)
	}
	if source != templates.SourceOfficial || version != "1.1.0" {
		t.Fatalf("provenance source=%q version=%q", source, version)
	}
	var portCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=? AND protocol='udp' AND port IN (16261,16262)`, serverID).Scan(&portCount); err != nil || portCount != 2 {
		t.Fatalf("provisioned ports=%d err=%v", portCount, err)
	}

	record, err = serverService.Start(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		current, getErr := serverService.Get(context.Background(), serverID)
		if getErr == nil && (current.Runtime.CurrentState == servers.StateRunning || current.Runtime.CurrentState == servers.StateStarting || current.Runtime.CurrentState == servers.StateStopping) {
			_, _ = serverService.Kill(context.Background(), serverID)
		}
	}()
	t.Logf("runtime started: pid=%d", record.Runtime.PID)
	session, ok := serverService.Console().CurrentSession(serverID)
	if !ok {
		t.Fatal("console session was not attached")
	}
	events, unsubscribe := session.Subscribe()
	defer unsubscribe()
	secretBytes := make([]byte, 18)
	if _, err = rand.Read(secretBytes); err != nil {
		t.Fatal(err)
	}
	adminPassword := hex.EncodeToString(secretBytes)
	transcript := ""
	passwordEntries := 0
	ready := false
	startDeadline := time.NewTimer(10 * time.Minute)
	defer startDeadline.Stop()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	consoleEvents := events
	observe := func(value string) {
		transcript = appendOutput(transcript, value, 64<<10)
		lower := strings.ToLower(transcript)
		if strings.Contains(lower, "enter new administrator password") && passwordEntries == 0 {
			if inputErr := session.Input(adminPassword + "\n"); inputErr != nil {
				t.Fatalf("first-boot administrator password input failed: %v", inputErr)
			}
			passwordEntries = 1
		}
		if strings.Contains(lower, "confirm the password") && passwordEntries == 1 {
			if inputErr := session.Input(adminPassword + "\n"); inputErr != nil {
				t.Fatalf("first-boot administrator password confirmation failed: %v", inputErr)
			}
			passwordEntries = 2
		}
		ready = strings.Contains(strings.ToUpper(transcript), "SERVER STARTED")
	}
	for !ready {
		select {
		case event, open := <-consoleEvents:
			if !open {
				// A noisy game may exceed a subscriber's bounded buffer. The
				// runtime session and stdin remain attached; continue from the
				// game's bounded on-disk console log.
				consoleEvents = nil
				continue
			}
			if event.Type == "output" {
				observe(event.Data)
			}
		case <-poll.C:
			if logData, readErr := os.ReadFile(filepath.Join(record.Server.WorkingDirectory, "server-console.txt")); readErr == nil {
				observe(string(logData))
			}
			current, getErr := serverService.Get(context.Background(), serverID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if current.Runtime.CurrentState == servers.StateCrashed || current.Runtime.CurrentState == servers.StateStopped {
				t.Fatalf("server exited before readiness; state=%s output tail=%s", current.Runtime.CurrentState, outputTail(transcript, 4000))
			}
		case <-startDeadline.C:
			t.Fatalf("server readiness timeout; output tail=%s", outputTail(transcript, 4000))
		}
	}
	t.Logf("console readiness observed; first-boot password prompts handled=%d", passwordEntries)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancelStop()
	record, err = serverService.Stop(stopCtx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Runtime.CurrentState != servers.StateStopped {
		t.Fatalf("runtime state after graceful stop = %s", record.Runtime.CurrentState)
	}
	exitCode := "unknown"
	if record.Runtime.ExitCode != nil {
		exitCode = fmt.Sprintf("%d", *record.Runtime.ExitCode)
	}
	t.Logf("graceful stdin stop completed; exit_code=%s", exitCode)
}

func appendOutput(existing, addition string, limit int) string {
	combined := existing + addition
	if len(combined) <= limit {
		return combined
	}
	return combined[len(combined)-limit:]
}

func outputTail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
