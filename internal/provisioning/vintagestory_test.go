package provisioning

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
	"gamenode/internal/vintagestory"
)

type fakeVintageStoryInstaller struct {
	mu    sync.Mutex
	plans []vintagestory.Plan
	err   error
}

func (i *fakeVintageStoryInstaller) Install(ctx context.Context, root string, plan vintagestory.Plan, output io.Writer, observe func(vintagestory.Event)) error {
	i.mu.Lock()
	i.plans = append(i.plans, plan)
	i.mu.Unlock()
	observe(vintagestory.Event{Phase: vintagestory.PhaseResolving, Summary: "resolving"})
	observe(vintagestory.Event{Phase: vintagestory.PhaseDownloading, Summary: "downloading"})
	if i.err != nil {
		return i.err
	}
	return os.WriteFile(filepath.Join(root, vintagestory.ServerDLL), []byte("dll"), 0o644)
}

func vintageStoryTemplate(t *testing.T) templates.Template {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "vintagestory", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var template templates.Template
	if err = json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	adapterData, err := os.ReadFile(filepath.Join("..", "..", "templates", "vintagestory", "serverconfig.adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var adapter templates.ConfigAdapterDefinition
	if err = json.Unmarshal(adapterData, &adapter); err != nil {
		t.Fatal(err)
	}
	template.ResolvedAdapters = []templates.ConfigAdapterDefinition{adapter}
	return template
}

// fakeDotnet points DOTNET_ROOT at a dummy file so DiscoverDotnet succeeds; the
// launch is only resolved, never executed, in these tests.
func fakeDotnet(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	name := "dotnet"
	if runtime.GOOS == "windows" {
		name = "dotnet.exe"
	}
	if err := os.WriteFile(filepath.Join(home, name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOTNET_ROOT", home)
}

func TestVintageStoryProvisioningRegistersOrdinaryNativeServer(t *testing.T) {
	fakeDotnet(t)
	for _, host := range []string{"linux", "windows"} {
		t.Run(host, func(t *testing.T) {
			db, data, _ := provisionFixture(t)
			defer db.Close()
			template := vintageStoryTemplate(t)
			installer := &fakeVintageStoryInstaller{}
			serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
			service := NewWithOptions(db, &templateSource{template: template}, nil, serverService, data, Options{HostOS: host})
			defer service.Close()
			service.SetVintageStoryInstaller(installer)
			preview, err := service.Check(context.Background(), template.ID)
			if err != nil || !preview.Provisionable || preview.Installer != templates.InstallerVintageStory || preview.AppID != 0 {
				t.Fatalf("preview=%#v err=%v", preview, err)
			}
			values := map[string]string{"VS_VERSION": "1.22.0-rc.10", "SERVER_PORT": "42500"}
			job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "VS " + host, DirectoryName: "vs-" + host, Values: values, ActorUserID: "actor"})
			if err != nil {
				t.Fatal(err)
			}
			job = waitTerminal(t, service, job.ID)
			if job.Status != Completed || job.ServerID == "" {
				t.Fatalf("job=%#v", job)
			}
			if len(installer.plans) != 1 || installer.plans[0].Version != "1.22.0-rc.10" {
				t.Fatalf("plans=%#v", installer.plans)
			}
			record, err := serverService.Get(context.Background(), job.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			if record.Server.RuntimeType != "native" || strings.Join(record.Server.Arguments, " ") != "VintagestoryServer.dll --dataPath data --port 42500" || record.Server.StopMethod != "stdin_command" || record.Server.StopCommand != "/stop" || record.Server.ConsoleLineEnding != "crlf" || len(record.Server.EnvironmentVariables) != 0 {
				t.Fatalf("server=%#v", record.Server)
			}
			var tcp, udp int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=? AND port=42500 AND protocol='tcp'`, job.ServerID).Scan(&tcp); err != nil || tcp != 1 {
				t.Fatalf("tcp ports=%d err=%v", tcp, err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=? AND port=42500 AND protocol='udp'`, job.ServerID).Scan(&udp); err != nil || udp != 1 {
				t.Fatalf("udp ports=%d err=%v", udp, err)
			}
			var adapters, steamRows int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_adapters WHERE server_id=? AND adapter_id='vintagestory-serverconfig'`, job.ServerID).Scan(&adapters); err != nil || adapters != 1 {
				t.Fatalf("the post-start adapter snapshot must be registered: %d %v", adapters, err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_steamcmd_provisioning WHERE server_id=?`, job.ServerID).Scan(&steamRows); err != nil || steamRows != 0 {
				t.Fatalf("not SteamCMD update-eligible: %d %v", steamRows, err)
			}
			if templateID, err := serverService.TemplateID(context.Background(), job.ServerID); err != nil || templateID != "vintage-story" {
				t.Fatalf("template provenance=%q err=%v", templateID, err)
			}
			if _, statErr := os.Stat(filepath.Join(defaultTenantServerRoot(t, data, "vs-"+host), "data", "serverconfig.json")); statErr == nil {
				t.Fatal("the server config is created by the game on first start, not seeded by GameNode")
			}
		})
	}
}

func TestVintageStoryProvisioningRejectsBadVersionsAndMissingInstaller(t *testing.T) {
	fakeDotnet(t)
	db, data, _ := provisionFixture(t)
	defer db.Close()
	template := vintageStoryTemplate(t)
	installer := &fakeVintageStoryInstaller{}
	service := NewWithOptions(db, &templateSource{template: template}, nil, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
	defer service.Close()
	if preview, err := service.Check(context.Background(), template.ID); err != nil || preview.Provisionable {
		t.Fatalf("without an installer the template must not be provisionable: %#v %v", preview, err)
	}
	service.SetVintageStoryInstaller(installer)
	for _, version := range []string{"", "1.20.4", "1.22", "1.22.7; rm -rf", "../1.22.7", "1.22.0-pre.1", "http://evil.example/x"} {
		if _, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Bad", DirectoryName: "bad", Values: map[string]string{"VS_VERSION": version}, ActorUserID: "actor"}); err == nil {
			t.Errorf("version %q must be rejected", version)
		}
	}
	if len(installer.plans) != 0 {
		t.Fatalf("the installer must never run for a rejected version: %#v", installer.plans)
	}
}

func TestVintageStoryInstallFailureHasControlledCodeAndNoServer(t *testing.T) {
	fakeDotnet(t)
	for err, code := range map[error]string{
		vintagestory.ErrVersionNotFound:   "VINTAGESTORY_VERSION_NOT_FOUND",
		vintagestory.ErrSourceUnavailable: "VINTAGESTORY_SOURCE_UNAVAILABLE",
		vintagestory.ErrDigestMismatch:    "VINTAGESTORY_DIGEST_MISMATCH",
		vintagestory.ErrRuntimeMissing:    "VINTAGESTORY_RUNTIME_MISSING",
		vintagestory.ErrExtractFailed:     "VINTAGESTORY_ARCHIVE_INVALID",
	} {
		db, data, _ := provisionFixture(t)
		template := vintageStoryTemplate(t)
		serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
		service := NewWithOptions(db, &templateSource{template: template}, nil, serverService, data, Options{HostOS: "linux"})
		service.SetVintageStoryInstaller(&fakeVintageStoryInstaller{err: err})
		job, startErr := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Fail", DirectoryName: "fail", Values: map[string]string{"VS_VERSION": "1.22.7"}, ActorUserID: "actor"})
		if startErr != nil {
			t.Fatal(startErr)
		}
		job = waitTerminal(t, service, job.ID)
		if job.Status != Failed || job.FailureCode != code || job.ServerID != "" {
			t.Fatalf("err=%v job=%#v", err, job)
		}
		if records, listErr := serverService.List(context.Background()); listErr != nil || len(records) != 0 {
			t.Fatalf("a failed install must not leave a server row: %v %v", records, listErr)
		}
		service.Close()
		db.Close()
	}
}
