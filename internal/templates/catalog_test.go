package templates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type catalogSourceStub struct {
	catalog   []byte
	templates map[string][]byte
	err       error
}

func (s *catalogSourceStub) FetchCatalog(context.Context) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.catalog, nil
}
func (s *catalogSourceStub) FetchTemplate(_ context.Context, name string) ([]byte, error) {
	data, ok := s.templates[name]
	if !ok {
		return nil, errors.New("missing template")
	}
	return data, nil
}

func officialFixture(t *testing.T, version string) ([]byte, []byte) {
	t.Helper()
	entry := CatalogEntry{ID: "minecraft-neoforge", Name: "Minecraft NeoForge", Description: "Safe server", Category: "minecraft", Version: version, TemplateSchemaVersion: 1, Platforms: []string{"windows", "linux"}, Installer: InstallerExisting, File: "minecraft/neoforge.json", Tags: []string{"minecraft"}, MinimumGameNode: "0.2.0"}
	manifest, _ := json.Marshal(CatalogManifest{SchemaVersion: 1, Templates: []CatalogEntry{entry}})
	template := Template{SchemaVersion: 1, ID: entry.ID, Name: entry.Name, Description: entry.Description, Version: version, Category: entry.Category, MinimumGameNode: "0.2.0", SourceType: SourceOfficial, SourceMetadata: SourceMetadata{Author: "GameNode"}, Installer: InstallerDefinition{Type: InstallerExisting}, Launch: &LaunchDefinition{Executable: "java", WorkingRoot: "server_root", Resolver: "neoforge", StopMethod: "stdin_command", StopCommand: "stop", StopTimeout: 60}, Variables: []TemplateVariable{}, Compatibility: Compatibility{Status: Compatible, Findings: []Finding{}}, ReadOnly: true, Platforms: entry.Platforms}
	templateData, _ := json.Marshal(template)
	return manifest, templateData
}

func TestCatalogDecodeValidation(t *testing.T) {
	valid, _ := officialFixture(t, "1.0.0")
	if manifest, err := decodeCatalog(valid); err != nil || len(manifest.Templates) != 1 {
		t.Fatalf("valid catalog: %#v %v", manifest, err)
	}
	cases := map[string][]byte{
		"invalid JSON":       []byte(`{"schema_version":`),
		"oversized":          make([]byte, MaxCatalogBytes+1),
		"unsupported schema": []byte(`{"schema_version":2,"templates":[]}`),
		"traversal":          []byte(`{"schema_version":1,"templates":[{"id":"x","name":"X","description":"x","category":"other","version":"1.0.0","template_schema_version":1,"platforms":["linux"],"installer":"existing","file":"../x.json","tags":[]}]}`),
		"absolute URL":       []byte(`{"schema_version":1,"templates":[{"id":"x","name":"X","description":"x","category":"other","version":"1.0.0","template_schema_version":1,"platforms":["linux"],"installer":"existing","file":"https://evil.example/x.json","tags":[]}]}`),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCatalog(data); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestOfficialTemplateSchemaAndCompatibility(t *testing.T) {
	manifestData, templateData := officialFixture(t, "1.0.0")
	manifest, _ := decodeCatalog(manifestData)
	if template, err := decodeOfficial(templateData, manifest.Templates[0], "0.2.0"); err != nil || template.SourceType != SourceOfficial || !template.ReadOnly {
		t.Fatalf("valid template: %#v %v", template, err)
	}
	var raw map[string]any
	_ = json.Unmarshal(templateData, &raw)
	raw["schema_version"] = float64(2)
	unknownSchema, _ := json.Marshal(raw)
	if _, err := decodeOfficial(unknownSchema, manifest.Templates[0], "0.2.0"); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("unknown schema: %v", err)
	}
	raw["schema_version"] = float64(1)
	raw["download_template_from"] = "https://evil.example/template.json"
	unknownField, _ := json.Marshal(raw)
	if _, err := decodeOfficial(unknownField, manifest.Templates[0], "0.2.0"); err == nil {
		t.Fatal("arbitrary URL field accepted")
	}
	compatible, err := decodeOfficial(templateData, manifest.Templates[0], "0.1.9")
	if err != nil || compatible.Compatibility.Status != Unsupported || len(compatible.Compatibility.Findings) == 0 {
		t.Fatalf("minimum version not enforced: %#v %v", compatible, err)
	}
	if !versionAtLeast("v0.2.1+build.4", "0.2.1") || versionAtLeast("v0.1.9", "0.2.0") {
		t.Fatal("version comparison is incorrect")
	}
}

func officialSteamFixture() Template {
	minimum, maximum := float64(1024), float64(65535)
	return Template{
		SchemaVersion: 1, ID: "steam-game", Name: "Steam Game", Description: "Safe dedicated server", Version: "1.0.0",
		Category: "steamcmd", SourceType: SourceOfficial, SourceMetadata: SourceMetadata{Author: "GameNode"}, ReadOnly: true,
		Platforms: []string{"windows", "linux"},
		Installer: InstallerDefinition{Type: InstallerSteamCMD, SteamCMD: &SteamCMDPlan{AppID: 294420, Validate: true, LoginMode: "anonymous", Platform: "native", InstallTarget: "server_root"}},
		PlatformLaunches: map[string]LaunchDefinition{
			"windows": {Executable: "Server.exe", Arguments: []string{"-port={{PORT}}"}, WorkingRoot: "server_root", StopMethod: "terminate", StopTimeout: 30},
			"linux":   {Executable: "./Server.x86_64", Arguments: []string{"-port={{PORT}}"}, WorkingRoot: "server_root", StopMethod: "terminate", StopTimeout: 30},
		},
		Variables:     []TemplateVariable{{Name: "Port", Key: "PORT", DefaultValue: "26900", UserEditable: true, Type: "integer", Required: true, Validation: Validation{Min: &minimum, Max: &maximum}}},
		Ports:         []TemplatePort{{Name: "Game", Protocol: "tcp", Variable: "PORT"}},
		Compatibility: Compatibility{Status: Compatible},
	}
}

func TestOfficialSteamTemplateValidation(t *testing.T) {
	valid := officialSteamFixture()
	if err := validateOfficial(valid); err != nil {
		t.Fatalf("valid SteamCMD template rejected: %v", err)
	}
	tests := map[string]func(*Template){
		"missing app id":    func(v *Template) { v.Installer.SteamCMD.AppID = 0 },
		"invalid app id":    func(v *Template) { v.Installer.SteamCMD.AppID = -1 },
		"unsupported login": func(v *Template) { v.Installer.SteamCMD.LoginMode = "user" },
		"absolute executable": func(v *Template) {
			launch := v.PlatformLaunches["windows"]
			launch.Executable = `C:\\game.exe`
			v.PlatformLaunches["windows"] = launch
		},
		"traversal executable": func(v *Template) {
			launch := v.PlatformLaunches["linux"]
			launch.Executable = "../game"
			v.PlatformLaunches["linux"] = launch
		},
		"dangerous executable": func(v *Template) {
			launch := v.PlatformLaunches["windows"]
			launch.Executable = "cmd.exe"
			v.PlatformLaunches["windows"] = launch
		},
		"missing windows launch": func(v *Template) { delete(v.PlatformLaunches, "windows") },
		"missing linux launch":   func(v *Template) { delete(v.PlatformLaunches, "linux") },
		"working directory escape": func(v *Template) {
			launch := v.PlatformLaunches["linux"]
			launch.WorkingDirectory = "../outside"
			v.PlatformLaunches["linux"] = launch
		},
		"invalid default":       func(v *Template) { v.Variables[0].DefaultValue = "not-a-port" },
		"invalid port variable": func(v *Template) { v.Ports[0].Variable = "APP_ID" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := officialSteamFixture()
			mutate(&candidate)
			if err := validateOfficial(candidate); err == nil {
				t.Fatal("unsafe SteamCMD template accepted")
			}
		})
	}
}

func TestStructuredArgumentsDoNotAcquireShellSemantics(t *testing.T) {
	template := officialSteamFixture()
	launch := template.PlatformLaunches["linux"]
	launch.Arguments = []string{"--message=operators & | ; are plain argv data"}
	template.PlatformLaunches["linux"] = launch
	if err := validateOfficial(template); err != nil {
		t.Fatalf("structured argument data was treated as a shell command: %v", err)
	}
}

func TestOfficialLaunchRejectsShellInterpreters(t *testing.T) {
	for _, executable := range []string{"cmd.exe", "powershell.exe", "pwsh.exe", "sh", "sh.exe", "bash", "bash.exe"} {
		t.Run(executable, func(t *testing.T) {
			template := officialSteamFixture()
			launch := template.PlatformLaunches["windows"]
			launch.Executable = executable
			launch.Arguments = []string{"-c", "server"}
			template.PlatformLaunches["windows"] = launch
			err := validateOfficial(template)
			if ValidationCode(err) != CodeShellSemanticsForbidden {
				t.Fatalf("shell interpreter accepted or wrong error: %v", err)
			}
		})
	}
}

func TestOfficialLaunchAllowsWindowsJavaClasspathAsOneArgument(t *testing.T) {
	launch := LaunchDefinition{
		Executable:  "jre64/bin/java.exe",
		Arguments:   []string{"-cp", "java/.;java/projectzomboid.jar", "zombie.network.GameServer"},
		WorkingRoot: "server_root",
		StopMethod:  "stdin_command",
		StopCommand: "quit",
		StopTimeout: 60,
	}
	if err := validateOfficialLaunch(launch, map[string]bool{}); err != nil {
		t.Fatalf("structured Java classpath was rejected: %v", err)
	}
	if len(launch.Arguments) != 3 || launch.Arguments[1] != "java/.;java/projectzomboid.jar" {
		t.Fatalf("classpath must remain one argv element: %#v", launch.Arguments)
	}
	launch.Arguments[1] = `java/.;C:\host\evil.jar`
	if err := validateOfficialLaunch(launch, map[string]bool{}); err == nil {
		t.Fatal("absolute Java classpath entry was accepted")
	}
}

func TestOfficialSteamTemplateRejectsUnknownInstallerFields(t *testing.T) {
	template := officialSteamFixture()
	data, _ := json.Marshal(template)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	installer := raw["installer"].(map[string]any)
	steam := installer["steamcmd"].(map[string]any)
	steam["command"] = "+app_update 1"
	data, _ = json.Marshal(raw)
	entry := CatalogEntry{ID: template.ID, Name: template.Name, Description: template.Description, Category: template.Category, Version: template.Version, TemplateSchemaVersion: 1, Platforms: template.Platforms, Installer: InstallerSteamCMD, File: "steamcmd/game.json"}
	if _, err := decodeOfficial(data, entry, "0.2.0"); err == nil {
		t.Fatal("unknown SteamCMD command field accepted")
	}
}

func TestOfficialConfigurationAdapterFetchValidationAndCache(t *testing.T) {
	template := officialSteamFixture()
	template.Configuration = &ConfigurationDefinition{Adapters: []ConfigAdapterReference{{ID: "serverconfig", SchemaVersion: 1, File: "serverconfig.adapter.json"}}}
	adapter := ConfigAdapterDefinition{SchemaVersion: 1, ID: "serverconfig", Version: "1.0.0", Format: "xml-properties", Target: "serverconfig.xml", RestartRequired: true, Fields: []ConfigAdapterField{{Key: "PORT", Label: "Game port", Type: "integer", Property: "ServerPort", Required: true, Validation: template.Variables[0].Validation}}}
	entry := CatalogEntry{ID: template.ID, Name: template.Name, Description: template.Description, Category: template.Category, Version: template.Version, TemplateSchemaVersion: 1, Platforms: template.Platforms, Installer: InstallerSteamCMD, File: "steamcmd/steam-game/template.json"}
	manifest, _ := json.Marshal(CatalogManifest{SchemaVersion: 1, Templates: []CatalogEntry{entry}})
	templateData, _ := json.Marshal(template)
	adapterData, _ := json.Marshal(adapter)
	directory := t.TempDir()
	source := &catalogSourceStub{catalog: manifest, templates: map[string][]byte{"steamcmd/steam-game/template.json": templateData, "steamcmd/steam-game/serverconfig.adapter.json": adapterData}}
	manager := NewCatalogManager(source, directory, "0.2.0")
	result, err := manager.Refresh(context.Background())
	if err != nil || len(result.Templates) != 1 || len(result.Templates[0].ResolvedAdapters) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err = os.Stat(filepath.Join(directory, "templates", "cache", "templates", "steamcmd", "steam-game", "serverconfig.adapter.json")); err != nil {
		t.Fatal("adapter was not cached")
	}
	reloaded := NewCatalogManager(&catalogSourceStub{err: errors.New("offline")}, directory, "0.2.0")
	if cached := reloaded.List(); len(cached.Templates) != 1 || len(cached.Templates[0].ResolvedAdapters) != 1 {
		t.Fatalf("cached adapter unavailable: %#v", cached)
	}
	source.templates["steamcmd/steam-game/serverconfig.adapter.json"] = []byte(`{"schema_version":1,"id":"serverconfig","version":"1.0.0","format":"xpath","target":"serverconfig.xml","restart_required":true,"fields":[]}`)
	if refreshed, refreshErr := manager.Refresh(context.Background()); refreshErr != nil || len(refreshed.Templates) != 1 || len(refreshed.Templates[0].ResolvedAdapters) != 1 {
		t.Fatalf("invalid adapter should preserve last-good adapter: %#v %v", refreshed, refreshErr)
	}
}

func TestOfficialPostStartINIAdapterAllowsValidatedConfigOnlyFields(t *testing.T) {
	template := officialSteamFixture()
	reference := ConfigAdapterReference{ID: "project-zomboid-server-ini", SchemaVersion: 1, File: "server.ini.adapter.json"}
	adapter := ConfigAdapterDefinition{SchemaVersion: 1, ID: reference.ID, Version: "1.0.0", Format: "ini-key-values", Target: "Server/gamenode.ini", RestartRequired: true, PostStartOnly: true, Fields: []ConfigAdapterField{{Key: "PZ_PUBLIC_NAME", Label: "Public name", Type: "string", Property: "PublicName", Required: true, Validation: Validation{MaxLength: intPointer(64)}}, {Key: "PZ_PASSWORD", Label: "Password", Type: "secret", Property: "Password", Nullable: true, Sensitive: true, Validation: Validation{MaxLength: intPointer(64)}}}}
	data, _ := json.Marshal(adapter)
	decoded, err := decodeConfigAdapter(data, reference, template)
	if err != nil || decoded.Format != "ini-key-values" || !decoded.PostStartOnly {
		t.Fatalf("post-start INI adapter rejected: %#v %v", decoded, err)
	}
	adapter.PostStartOnly = false
	data, _ = json.Marshal(adapter)
	if _, err = decodeConfigAdapter(data, reference, template); err == nil {
		t.Fatal("config-only field without post-start lifecycle was accepted")
	}
	adapter.PostStartOnly = true
	adapter.Target = "Server/../../outside.ini"
	data, _ = json.Marshal(adapter)
	if _, err = decodeConfigAdapter(data, reference, template); err == nil {
		t.Fatal("traversal INI target was accepted")
	}
}

func intPointer(value int) *int { return &value }

func TestOfficialConfigurationReferenceRejectsPathEscape(t *testing.T) {
	for _, file := range []string{"../adapter.json", "sub/adapter.json", "https://evil.example/adapter.json", `C:\\adapter.json`} {
		if err := validateConfigurationReferences(&ConfigurationDefinition{Adapters: []ConfigAdapterReference{{ID: "safe", SchemaVersion: 1, File: file}}}); err == nil {
			t.Fatalf("unsafe adapter path accepted: %q", file)
		}
	}
}

func TestHTTPSourceSafetyStatusSizeTimeoutAndRedirect(t *testing.T) {
	if _, err := NewHTTPSource("http://example.test/templates/", nil); err == nil {
		t.Fatal("HTTP source accepted")
	}
	t.Run("status", func(t *testing.T) {
		server := httptest.NewTLSServer(http.NotFoundHandler())
		defer server.Close()
		source, _ := NewHTTPSource(server.URL+"/", server.Client())
		if _, err := source.FetchCatalog(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("status error = %v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, MaxCatalogBytes+1)) }))
		defer server.Close()
		source, _ := NewHTTPSource(server.URL+"/", server.Client())
		if _, err := source.FetchCatalog(context.Background()); err == nil {
			t.Fatal("oversized response accepted")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		client := server.Client()
		client.Timeout = 20 * time.Millisecond
		source, _ := NewHTTPSource(server.URL+"/", client)
		if _, err := source.FetchCatalog(context.Background()); err == nil {
			t.Fatal("timeout not enforced")
		}
	})
	t.Run("foreign redirect", func(t *testing.T) {
		target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer target.Close()
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
		defer server.Close()
		source, _ := NewHTTPSource(server.URL+"/", server.Client())
		if _, err := source.FetchCatalog(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect rejected") {
			t.Fatalf("redirect error = %v", err)
		}
	})
}

func TestCatalogCacheFallbackReplacementAndCorruption(t *testing.T) {
	directory := t.TempDir()
	manifestV1, templateV1 := officialFixture(t, "1.0.0")
	source := &catalogSourceStub{catalog: manifestV1, templates: map[string][]byte{"minecraft/neoforge.json": templateV1}}
	manager := NewCatalogManager(source, directory, "0.2.0")
	first, err := manager.Refresh(context.Background())
	if err != nil || len(first.Templates) != 1 || first.Status.Source != "remote" {
		t.Fatalf("initial refresh: %#v %v", first, err)
	}
	if _, err = os.Stat(filepath.Join(directory, "templates", "cache", "catalog.json")); err != nil {
		t.Fatal("cache was not written")
	}
	offline := NewCatalogManager(&catalogSourceStub{err: errors.New("offline")}, directory, "0.2.0")
	if result := offline.List(); len(result.Templates) != 1 || !result.Status.Cached || result.Status.Offline {
		t.Fatalf("offline cache: %#v", result)
	}
	if result, refreshErr := offline.Refresh(context.Background()); refreshErr == nil || len(result.Templates) != 1 || !result.Status.Offline {
		t.Fatalf("failed refresh discarded cache: %#v %v", result, refreshErr)
	}
	manifestV2, templateV2 := officialFixture(t, "1.1.0")
	source.catalog, source.templates = manifestV2, map[string][]byte{"minecraft/neoforge.json": templateV2}
	if result, refreshErr := manager.Refresh(context.Background()); refreshErr != nil || result.Templates[0].Version != "1.1.0" {
		t.Fatalf("stale replacement: %#v %v", result, refreshErr)
	}
	reloaded := NewCatalogManager(source, directory, "0.2.0")
	if result := reloaded.List(); len(result.Templates) != 1 || result.Templates[0].Version != "1.1.0" {
		t.Fatalf("reloaded replacement: %#v", result)
	}
	if err = os.WriteFile(filepath.Join(directory, "templates", "cache", "catalog.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := NewCatalogManager(&catalogSourceStub{err: errors.New("offline")}, directory, "0.2.0")
	if result := corrupt.List(); len(result.Templates) != 0 || result.Status.Source != "none" {
		t.Fatalf("corrupted cache accepted: %#v", result)
	}
	if result, refreshErr := corrupt.Refresh(context.Background()); refreshErr == nil || len(result.Templates) != 0 || !result.Status.Offline || result.Status.Cached {
		t.Fatalf("no-cache fallback: %#v %v", result, refreshErr)
	}
}

func TestCatalogPartialMalformedTemplate(t *testing.T) {
	manifestData, valid := officialFixture(t, "1.0.0")
	var manifest CatalogManifest
	_ = json.Unmarshal(manifestData, &manifest)
	manifest.Templates = append(manifest.Templates, CatalogEntry{ID: "broken", Name: "Broken", Description: "broken", Category: "other", Version: "1.0.0", TemplateSchemaVersion: 1, Platforms: []string{"linux"}, Installer: InstallerExisting, File: "other/broken.json", Tags: []string{}})
	manifestData, _ = json.Marshal(manifest)
	manager := NewCatalogManager(&catalogSourceStub{catalog: manifestData, templates: map[string][]byte{"minecraft/neoforge.json": valid, "other/broken.json": []byte("{")}}, t.TempDir(), "0.2.0")
	result, err := manager.Refresh(context.Background())
	if err != nil || len(result.Templates) != 1 || result.Status.InvalidTemplates != 1 {
		t.Fatalf("partial catalog: %#v %v", result, err)
	}
}

func TestRepositoryOfficialCatalog(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodeCatalog(data)
	if err != nil || len(manifest.Templates) == 0 {
		t.Fatalf("repository catalog: %#v %v", manifest, err)
	}
	for _, entry := range manifest.Templates {
		data, err = os.ReadFile(filepath.Join("..", "..", "templates", filepath.FromSlash(entry.File)))
		if err != nil {
			t.Fatal(err)
		}
		template, decodeErr := decodeOfficial(data, entry, "0.2.0")
		if decodeErr != nil {
			t.Fatalf("repository template %s: %v", entry.ID, decodeErr)
		}
		if template.ID != entry.ID || template.Version != entry.Version || template.SchemaVersion != entry.TemplateSchemaVersion || len(template.ExpectedFiles) == 0 {
			t.Fatalf("repository template metadata mismatch: %#v", template)
		}
		if template.Configuration != nil {
			for _, reference := range template.Configuration.Adapters {
				adapterData, readErr := os.ReadFile(filepath.Join("..", "..", "templates", filepath.Dir(filepath.FromSlash(entry.File)), reference.File))
				if readErr != nil {
					t.Fatal(readErr)
				}
				if _, adapterErr := decodeConfigAdapter(adapterData, reference, template); adapterErr != nil {
					t.Fatalf("repository adapter %s/%s: %v", entry.ID, reference.ID, adapterErr)
				}
			}
		}
	}
	wanted := map[string]struct {
		installer string
		platforms int
		resolver  string
	}{"7-days-to-die": {InstallerSteamCMD, 2, ""}, "project-zomboid": {InstallerSteamCMD, 1, ""}, "minecraft-neoforge": {InstallerExistingFiles, 2, "neoforge"}, "palworld": {InstallerSteamCMD, 1, ""}, "satisfactory": {InstallerSteamCMD, 1, ""}, "eco": {InstallerSteamCMD, 2, ""}, "valheim": {InstallerSteamCMD, 1, ""}, "vein": {InstallerSteamCMD, 1, ""}}
	for _, entry := range manifest.Templates {
		expected, ok := wanted[entry.ID]
		if !ok || entry.Installer != expected.installer || len(entry.Platforms) != expected.platforms {
			t.Fatalf("unexpected golden catalog entry: %#v", entry)
		}
		if expected.resolver != "" {
			data, _ := os.ReadFile(filepath.Join("..", "..", "templates", filepath.FromSlash(entry.File)))
			template, _ := decodeOfficial(data, entry, "0.2.0")
			if template.Launch == nil || template.Launch.Resolver != expected.resolver {
				t.Fatalf("unexpected resolver for %s", entry.ID)
			}
		}
	}
}

func TestEcoRepositoryGolden(t *testing.T) {
	templateData, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "eco", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry := CatalogEntry{ID: "eco", Name: "Eco Dedicated Server", Description: "Install the official Eco dedicated server through SteamCMD and run it natively in offline mode without exposing account credentials.", Category: "steamcmd", Version: "1.0.0", TemplateSchemaVersion: 2, Platforms: []string{"windows", "linux"}, Installer: InstallerSteamCMD, File: "steamcmd/eco/template.json", Tags: []string{"eco", "steam", "steamcmd", "simulation", "survival"}, Icon: "steamcmd", MinimumGameNode: "0.2.0"}
	template, err := decodeOfficial(templateData, entry, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	windows := template.PlatformLaunches["windows"]
	linux := template.PlatformLaunches["linux"]
	if template.Installer.SteamCMD == nil || template.Installer.SteamCMD.AppID != 739590 || template.Compatibility.Status != PartiallyCompatible || windows.Executable != "EcoServer.exe" || linux.Executable != "EcoServer" || len(windows.Arguments) != 2 || windows.Arguments[0] != "--nogui" || windows.Arguments[1] != "-offline" || len(template.Ports) != 2 || template.Ports[0].Port != 3000 || template.Ports[0].Protocol != "udp" || template.Ports[1].Port != 3001 || template.Ports[1].Protocol != "tcp" {
		t.Fatalf("unexpected Eco template: %#v windows=%#v linux=%#v", template, windows, linux)
	}
}

func TestOfficialV2RequiresValidationObjectEvenForBooleanVariables(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "palworld", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	variables := raw["variables"].([]any)
	for _, item := range variables {
		variable := item.(map[string]any)
		if variable["key"] == "RCON_ENABLED" {
			delete(variable, "validation")
			break
		}
	}
	data, _ = json.Marshal(raw)
	entry := CatalogEntry{ID: "palworld", Name: "Palworld Dedicated Server", Description: "Install the official Palworld dedicated server through SteamCMD and run the Windows native server binary directly.", Category: "steamcmd", Version: "1.1.0", TemplateSchemaVersion: 2, Platforms: []string{"windows"}, Installer: InstallerSteamCMD, File: "steamcmd/palworld/template.json", Tags: []string{"palworld", "steam", "steamcmd", "survival"}, Icon: "steamcmd", MinimumGameNode: "0.2.0"}
	if _, err = decodeOfficial(data, entry, "0.2.0"); ValidationCode(err) != CodeSchemaInvalid {
		t.Fatalf("missing validation object accepted: %v", err)
	}
}

func TestPalworldRepositoryGolden(t *testing.T) {
	templateData, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "palworld", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry := CatalogEntry{ID: "palworld", Name: "Palworld Dedicated Server", Description: "Install the official Palworld dedicated server through SteamCMD and run the Windows native server binary directly.", Category: "steamcmd", Version: "1.1.0", TemplateSchemaVersion: 2, Platforms: []string{"windows"}, Installer: InstallerSteamCMD, File: "steamcmd/palworld/template.json", Tags: []string{"palworld", "steam", "steamcmd", "survival"}, Icon: "steamcmd", MinimumGameNode: "0.2.0"}
	template, err := decodeOfficial(templateData, entry, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	launch := template.PlatformLaunches["windows"]
	if template.ID != "palworld" || template.SchemaVersion != 2 || template.Installer.SteamCMD == nil || template.Installer.SteamCMD.AppID != 2394010 || len(template.Platforms) != 1 || template.Platforms[0] != "windows" || launch.Executable != "PalServer.exe" || len(launch.Arguments) != 3 || launch.Arguments[0] != "-port={{SERVER_PORT}}" || len(template.Ports) != 3 || len(template.ExpectedFiles) != 3 {
		t.Fatalf("unexpected Palworld template: %#v launch=%#v", template, launch)
	}
	adapterData, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "palworld", "palworld-settings.adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := decodeConfigAdapter(adapterData, template.Configuration.Adapters[0], template)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.ID != "palworld-settings" || adapter.Format != "section-tuple-key-values" || adapter.Section != "/Script/Pal.PalGameWorldSettings" || adapter.ContainerProperty != "OptionSettings" || adapter.Initialization == nil || adapter.Initialization.Mode != "seed-from-file" || adapter.Initialization.Source != "DefaultPalWorldSettings.ini" || len(adapter.Fields) != 9 {
		t.Fatalf("unexpected Palworld adapter: %#v", adapter)
	}
}

func TestSatisfactoryRepositoryGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "satisfactory", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry := CatalogEntry{ID: "satisfactory", Name: "Satisfactory Dedicated Server", Description: "Install the official Satisfactory dedicated server through SteamCMD and launch its native Windows server executable without a shell.", Category: "steamcmd", Version: "1.0.0", TemplateSchemaVersion: 2, Platforms: []string{"windows"}, Installer: InstallerSteamCMD, File: "steamcmd/satisfactory/template.json", Tags: []string{"satisfactory", "steam", "steamcmd", "factory", "survival"}, Icon: "steamcmd", MinimumGameNode: "0.2.0"}
	template, err := decodeOfficial(data, entry, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	windows, windowsOK := template.PlatformLaunches["windows"]
	_, linuxOK := template.PlatformLaunches["linux"]
	if template.Installer.SteamCMD == nil || template.Installer.SteamCMD.AppID != 1690800 || template.Installer.SteamCMD.BetaBranchVariable != "RELEASE_BRANCH" || !windowsOK || linuxOK || windows.Executable != "FactoryServer.exe" || len(template.Ports) != 3 || len(template.ExpectedFiles) != 3 || template.Configuration != nil || template.Compatibility.Status != PartiallyCompatible || len(template.Platforms) != 1 || template.Platforms[0] != "windows" {
		t.Fatalf("unexpected Satisfactory template: %#v", template)
	}
	for _, variable := range template.Variables {
		if variable.Key == "SERVER_NAME" || variable.Key == "SERVER_PASSWORD" || variable.Key == "ADMIN_PASSWORD" {
			t.Fatalf("ineffective Satisfactory variable exposed: %s", variable.Key)
		}
	}
}

func TestDocumentationExampleAndJSONSchemas(t *testing.T) {
	documentation, err := os.ReadFile(filepath.Join("..", "..", "docs", "templates.md"))
	if err != nil {
		t.Fatal(err)
	}
	marker := "## Complete SteamCMD example"
	section := strings.SplitN(string(documentation), marker, 2)
	if len(section) != 2 {
		t.Fatal("documentation example section is missing")
	}
	blocks := strings.SplitN(section[1], "```json\n", 2)
	if len(blocks) != 2 {
		t.Fatal("documentation JSON example is missing")
	}
	example := strings.SplitN(blocks[1], "\n```", 2)[0]
	entry := CatalogEntry{ID: "example-steam-server", Name: "Example Steam Server", Description: "Contributor example for a direct native dedicated server.", Category: "steamcmd", Version: "1.0.0", TemplateSchemaVersion: 2, Platforms: []string{"windows"}, Installer: InstallerSteamCMD, File: "examples/example-steam-server.json", Tags: []string{"example", "steamcmd"}}
	if _, err = decodeOfficial([]byte(example), entry, "0.2.0"); err != nil {
		t.Fatalf("documented template does not validate: %v", err)
	}
	for _, name := range []string{"template.schema.json", "catalog.schema.json"} {
		data, readErr := os.ReadFile(filepath.Join("..", "..", "templates", "schema", name))
		if readErr != nil || !json.Valid(data) {
			t.Fatalf("invalid JSON schema %s: %v", name, readErr)
		}
		var schema map[string]any
		if json.Unmarshal(data, &schema) != nil || schema["$schema"] == nil || schema["$id"] == nil {
			t.Fatalf("JSON schema metadata missing: %s", name)
		}
	}
}
