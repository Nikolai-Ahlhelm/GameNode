package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"gamenode/internal/minecraft"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

type fakeMinecraftInstaller struct {
	mu    sync.Mutex
	plans []minecraft.Plan
	err   error
	files map[string]string
}

func (i *fakeMinecraftInstaller) Install(ctx context.Context, root string, plan minecraft.Plan, output io.Writer, observe func(minecraft.Event)) error {
	i.mu.Lock()
	i.plans = append(i.plans, plan)
	i.mu.Unlock()
	observe(minecraft.Event{Phase: minecraft.PhaseResolving, Summary: "resolving"})
	observe(minecraft.Event{Phase: minecraft.PhaseDownloading, Summary: "downloading"})
	if i.err != nil {
		return i.err
	}
	for relative, content := range i.files {
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func minecraftTemplate(t *testing.T) templates.Template {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "minecraft", "java", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var template templates.Template
	if err = json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	return template
}

// fakeJava points JAVA_HOME at a dummy file so DiscoverJava succeeds without a
// real JDK; the launch is only resolved, never executed, in these tests.
func fakeJava(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	name := "java"
	if runtime.GOOS == "windows" {
		name = "java.exe"
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bin", name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JAVA_HOME", home)
}

func TestMinecraftProvisioningInstallsAndRegistersOrdinaryNativeServer(t *testing.T) {
	fakeJava(t)
	for _, host := range []string{"linux", "windows"} {
		t.Run(host, func(t *testing.T) {
			db, data, _ := provisionFixture(t)
			defer db.Close()
			template := minecraftTemplate(t)
			installer := &fakeMinecraftInstaller{files: map[string]string{"server.jar": "jar"}}
			serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
			service := NewWithOptions(db, &templateSource{template: template}, nil, serverService, data, Options{HostOS: host})
			defer service.Close()
			service.SetMinecraftInstaller(installer)
			preview, err := service.Check(context.Background(), template.ID)
			if err != nil || !preview.Provisionable || preview.Installer != templates.InstallerMinecraft || preview.AppID != 0 {
				t.Fatalf("preview=%#v err=%v", preview, err)
			}
			values := map[string]string{"LOADER": "fabric", "MINECRAFT_VERSION": "1.21.1", "LOADER_VERSION": "0.19.5", "SERVER_PORT": "25570", "MAX_MEMORY_MB": "2048"}
			job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Fabric " + host, DirectoryName: "fabric-" + host, Values: values, ActorUserID: "actor"})
			if err != nil {
				t.Fatal(err)
			}
			if job.InstallerType != templates.InstallerMinecraft || job.AppID != 0 {
				t.Fatalf("job=%#v", job)
			}
			job = waitTerminal(t, service, job.ID)
			if job.Status != Completed || job.ServerID == "" {
				t.Fatalf("job=%#v", job)
			}
			if len(installer.plans) != 1 || installer.plans[0] != (minecraft.Plan{Loader: "fabric", MinecraftVersion: "1.21.1", LoaderVersion: "0.19.5"}) {
				t.Fatalf("installer plans=%#v", installer.plans)
			}
			record, err := serverService.Get(context.Background(), job.ServerID)
			if err != nil {
				t.Fatal(err)
			}
			wantArguments := "-Xms1024M -Xmx2048M -jar server.jar nogui"
			if record.Server.RuntimeType != "native" || record.Server.CreationMode != servers.CreationTemplate || strings.Join(record.Server.Arguments, " ") != wantArguments || record.Server.StopMethod != "stdin_command" || record.Server.StopCommand != "stop" {
				t.Fatalf("server=%#v", record.Server)
			}
			properties, err := os.ReadFile(filepath.Join(defaultTenantServerRoot(t, data, "fabric-"+host), "server.properties"))
			if err != nil || string(properties) != "server-port=25570\n" {
				t.Fatalf("server.properties=%q err=%v", properties, err)
			}
			var portCount int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_ports WHERE server_id=? AND port=25570`, job.ServerID).Scan(&portCount); err != nil || portCount != 1 {
				t.Fatalf("ports=%d err=%v", portCount, err)
			}
			var steamRows int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_steamcmd_provisioning WHERE server_id=?`, job.ServerID).Scan(&steamRows); err != nil || steamRows != 0 {
				t.Fatalf("a Minecraft server must not become SteamCMD update-eligible: rows=%d err=%v", steamRows, err)
			}
			if templateID, err := serverService.TemplateID(context.Background(), job.ServerID); err != nil || templateID != "minecraft-java" {
				t.Fatalf("template provenance=%q err=%v", templateID, err)
			}
		})
	}
}

func TestMinecraftProvisioningRejectsBadSelectionsBeforeReservingTarget(t *testing.T) {
	fakeJava(t)
	db, data, _ := provisionFixture(t)
	defer db.Close()
	template := minecraftTemplate(t)
	installer := &fakeMinecraftInstaller{}
	service := NewWithOptions(db, &templateSource{template: template}, nil, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
	defer service.Close()
	service.SetMinecraftInstaller(installer)
	for name, values := range map[string]map[string]string{
		"vanilla with loader version": {"LOADER": "vanilla", "MINECRAFT_VERSION": "1.21.1", "LOADER_VERSION": "1.0"},
		"fabric without version":      {"LOADER": "fabric", "MINECRAFT_VERSION": "1.21.1"},
		"neoforge for wrong game":     {"LOADER": "neoforge", "MINECRAFT_VERSION": "1.20.4", "LOADER_VERSION": "21.1.77"},
		"forge before 1.17":           {"LOADER": "forge", "MINECRAFT_VERSION": "1.12.2", "LOADER_VERSION": "14.23.5.2860"},
		"traversal in loader version": {"LOADER": "fabric", "MINECRAFT_VERSION": "1.21.1", "LOADER_VERSION": "../../x"},
		"url as minecraft version":    {"LOADER": "vanilla", "MINECRAFT_VERSION": "http://evil.example/a"},
		"unknown loader":              {"LOADER": "quilt", "MINECRAFT_VERSION": "1.21.1"},
	} {
		if _, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Bad", DirectoryName: "bad", Values: values, ActorUserID: "actor"}); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	if len(installer.plans) != 0 {
		t.Fatalf("installer must never run for rejected selections: %#v", installer.plans)
	}
	if _, err := os.Stat(defaultTenantServerRoot(t, data, "bad")); err == nil {
		t.Fatal("a rejected selection must not create a server directory")
	}
}

func TestMinecraftInstallFailureHasControlledCodeAndNoServer(t *testing.T) {
	fakeJava(t)
	for err, wantCode := range map[error]string{
		minecraft.ErrVersionNotFound:   "MINECRAFT_VERSION_NOT_FOUND",
		minecraft.ErrSourceUnavailable: "MINECRAFT_SOURCE_UNAVAILABLE",
		minecraft.ErrDigestMismatch:    "MINECRAFT_DIGEST_MISMATCH",
		minecraft.ErrInstallerFailed:   "MINECRAFT_INSTALLER_FAILED",
		errors.New("secret detail"):    "MINECRAFT_INSTALL_FAILED",
	} {
		db, data, _ := provisionFixture(t)
		template := minecraftTemplate(t)
		serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
		service := NewWithOptions(db, &templateSource{template: template}, nil, serverService, data, Options{HostOS: "linux"})
		service.SetMinecraftInstaller(&fakeMinecraftInstaller{err: err})
		job, startErr := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Fail", DirectoryName: "fail", Values: map[string]string{"LOADER": "vanilla", "MINECRAFT_VERSION": "1.21.1"}, ActorUserID: "actor"})
		if startErr != nil {
			t.Fatal(startErr)
		}
		job = waitTerminal(t, service, job.ID)
		if job.Status != Failed || job.FailureCode != wantCode || job.ServerID != "" || strings.Contains(job.ErrorSummary, "secret detail") {
			t.Fatalf("err=%v job=%#v", err, job)
		}
		if records, listErr := serverService.List(context.Background()); listErr != nil || len(records) != 0 {
			t.Fatalf("a failed install must not leave a server row: %v %v", records, listErr)
		}
		service.Close()
		db.Close()
	}
}

func TestMinecraftProvisioningUnavailableWithoutInstaller(t *testing.T) {
	fakeJava(t)
	db, data, _ := provisionFixture(t)
	defer db.Close()
	template := minecraftTemplate(t)
	service := NewWithOptions(db, &templateSource{template: template}, nil, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()), data, Options{HostOS: "linux"})
	defer service.Close()
	preview, err := service.Check(context.Background(), template.ID)
	if err != nil || preview.Provisionable {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
}

func minecraftTemplateWithAdapter(t *testing.T) templates.Template {
	t.Helper()
	template := minecraftTemplate(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "minecraft", "java", "server.properties.adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var adapter templates.ConfigAdapterDefinition
	if err = json.Unmarshal(data, &adapter); err != nil {
		t.Fatal(err)
	}
	template.ResolvedAdapters = []templates.ConfigAdapterDefinition{adapter}
	return template
}

func TestMinecraftProvisioningWritesFullServerPropertiesAndHonorsEULAChoice(t *testing.T) {
	fakeJava(t)
	for _, accept := range []bool{false, true} {
		name := "declined"
		if accept {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			db, data, _ := provisionFixture(t)
			defer db.Close()
			template := minecraftTemplateWithAdapter(t)
			serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
			service := NewWithOptions(db, &templateSource{template: template}, nil, serverService, data, Options{HostOS: "linux"})
			defer service.Close()
			service.SetMinecraftInstaller(&fakeMinecraftInstaller{files: map[string]string{"server.jar": "jar"}})
			values := map[string]string{"LOADER": "vanilla", "MINECRAFT_VERSION": "1.21.1", "SERVER_PORT": "25571", "MC_MOTD": "My Server", "MC_DIFFICULTY": "hard", "MC_PVP": "false", "MC_MAX_PLAYERS": "8", "MC_RCON_PASSWORD": "s3cret-value"}
			if accept {
				values["ACCEPT_EULA"] = "true"
			}
			job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Props " + name, DirectoryName: "props-" + name, Values: values, ActorUserID: "actor"})
			if err != nil {
				t.Fatal(err)
			}
			job = waitTerminal(t, service, job.ID)
			if job.Status != Completed {
				t.Fatalf("job=%#v", job)
			}
			root := defaultTenantServerRoot(t, data, "props-"+name)
			properties, err := os.ReadFile(filepath.Join(root, "server.properties"))
			if err != nil {
				t.Fatal(err)
			}
			text := string(properties)
			for _, want := range []string{"server-port=25571\n", "motd=My Server\n", "difficulty=hard\n", "pvp=false\n", "max-players=8\n", "online-mode=true\n", "view-distance=10\n", "level-name=world\n"} {
				if !strings.Contains(text, want) {
					t.Fatalf("server.properties lacks %q:\n%s", want, text)
				}
			}
			if strings.Count(text, "=") < 55 {
				t.Fatalf("the whole default configuration must be written, got %d keys", strings.Count(text, "="))
			}
			eula, eulaErr := os.ReadFile(filepath.Join(root, "eula.txt"))
			if accept && (eulaErr != nil || !strings.Contains(string(eula), "eula=true")) {
				t.Fatalf("explicit opt-in must write eula=true: %q %v", eula, eulaErr)
			}
			if !accept && eulaErr == nil {
				t.Fatalf("the EULA must not be written without opt-in: %q", eula)
			}
			var secrets int
			if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_adapters WHERE server_id=? AND definition_json LIKE '%s3cret-value%'`, job.ServerID).Scan(&secrets); err != nil || secrets != 0 {
				t.Fatalf("a secret value must not enter the adapter snapshot: %d %v", secrets, err)
			}
		})
	}
}
