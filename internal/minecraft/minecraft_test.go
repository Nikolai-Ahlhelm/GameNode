package minecraft

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/filesystem"
)

func testEndpoints(server *httptest.Server) Endpoints {
	parsed, _ := url.Parse(server.URL)
	return Endpoints{
		MojangManifest: server.URL + "/mojang/manifest.json",
		NeoForgeAPI:    server.URL + "/neoforge/api",
		NeoForgeMaven:  server.URL + "/neoforge/maven",
		ForgeMetadata:  server.URL + "/forge/maven-metadata.xml",
		ForgeMaven:     server.URL + "/forge/maven",
		FabricMeta:     server.URL + "/fabric/v2/versions",
		AllowedHosts:   []string{parsed.Hostname()},
		AllowInsecure:  true,
	}
}

func upstream(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	jar := []byte("vanilla-server-jar-bytes")
	sum := sha1.Sum(jar)
	digest := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/mojang/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"versions":[{"id":"1.21.1","type":"release","url":"%s/mojang/1.21.1.json"},{"id":"1.21.2-snapshot","type":"snapshot","url":"x"},{"id":"1.20.4","type":"release","url":"%s/mojang/1.20.4.json"}]}`, server.URL, server.URL)
	})
	mux.HandleFunc("/mojang/1.21.1.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"downloads":{"server":{"url":"%s/mojang/server.jar","sha1":"%s"}}}`, server.URL, digest)
	})
	mux.HandleFunc("/mojang/1.20.4.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"downloads":{"server":{"url":"%s/mojang/server.jar","sha1":"%s"}}}`, server.URL, strings.Repeat("0", 40))
	})
	mux.HandleFunc("/mojang/server.jar", func(w http.ResponseWriter, r *http.Request) { w.Write(jar) })
	mux.HandleFunc("/neoforge/api", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"versions":["0.25w14craftmine.3-beta","20.2.3-beta","21.0.7","21.1.76","21.1.77","21.1.78-beta","26.3.0.45-beta"]}`)
	})
	mux.HandleFunc("/forge/maven-metadata.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<metadata><versioning><versions><version>1.12.2-14.23.5.2860</version><version>1.20.1-47.4.0</version><version>1.20.1-47.4.1</version><version>26.3-66.0.9</version></versions></versioning></metadata>`)
	})
	mux.HandleFunc("/fabric/v2/versions/game", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"version":"1.21.1","stable":true},{"version":"24w14a","stable":false}]`)
	})
	mux.HandleFunc("/fabric/v2/versions/loader/1.21.1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"loader":{"version":"0.19.5-beta.1","stable":false}},{"loader":{"version":"0.19.5","stable":true}},{"loader":{"version":"0.19.4","stable":true}}]`)
	})
	mux.HandleFunc("/fabric/v2/versions/installer", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"version":"1.1.2","stable":true}]`)
	})
	mux.HandleFunc("/fabric/v2/versions/loader/1.21.1/0.19.5/1.1.2/server/jar", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("fabric-launch-jar")) })
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, jar
}

func TestVersionDiscovery(t *testing.T) {
	server, _ := upstream(t)
	source := NewTestSource(testEndpoints(server))
	ctx := context.Background()
	vanilla, err := source.GameVersions(ctx, LoaderVanilla)
	if err != nil || strings.Join(vanilla, ",") != "1.21.1,1.20.4" {
		t.Fatalf("vanilla = %v, %v", vanilla, err)
	}
	neoGames, _ := source.GameVersions(ctx, LoaderNeoForge)
	if strings.Join(neoGames, ",") != "26.3,1.21.1,1.21,1.20.2" {
		t.Fatalf("neoforge games = %v", neoGames)
	}
	forgeGames, _ := source.GameVersions(ctx, LoaderForge)
	if strings.Join(forgeGames, ",") != "26.3,1.20.1" {
		t.Fatalf("forge games (1.12.2 must be excluded) = %v", forgeGames)
	}
	fabricGames, _ := source.GameVersions(ctx, LoaderFabric)
	if strings.Join(fabricGames, ",") != "1.21.1" {
		t.Fatalf("fabric games = %v", fabricGames)
	}
	neo, _ := source.LoaderVersions(ctx, LoaderNeoForge, "1.21.1")
	if len(neo) != 3 || neo[0].Version != "21.1.78-beta" || neo[0].Stable || neo[1].Version != "21.1.77" || !neo[1].Latest {
		t.Fatalf("neoforge builds = %+v", neo)
	}
	forge, _ := source.LoaderVersions(ctx, LoaderForge, "1.20.1")
	if len(forge) != 2 || forge[0].Version != "47.4.1" || !forge[0].Latest {
		t.Fatalf("forge builds = %+v", forge)
	}
	fabric, _ := source.LoaderVersions(ctx, LoaderFabric, "1.21.1")
	if len(fabric) != 3 || fabric[1].Version != "0.19.5" || !fabric[1].Latest || fabric[0].Latest {
		t.Fatalf("fabric builds = %+v", fabric)
	}
	if _, err = source.LoaderVersions(ctx, LoaderFabric, "../etc"); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("path-like version must be rejected, got %v", err)
	}
}

func TestSourceRejectsDisallowedHostAndPlainHTTP(t *testing.T) {
	server, _ := upstream(t)
	endpoints := testEndpoints(server)
	endpoints.AllowedHosts = []string{"example.invalid"}
	if _, err := NewTestSource(endpoints).GameVersions(context.Background(), LoaderVanilla); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("disallowed host must fail, got %v", err)
	}
	endpoints = testEndpoints(server)
	endpoints.AllowInsecure = false
	if _, err := NewTestSource(endpoints).GameVersions(context.Background(), LoaderVanilla); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("plain HTTP must fail, got %v", err)
	}
}

func TestInstallVanillaAndFabricVerifyAndPlace(t *testing.T) {
	server, jar := upstream(t)
	installer := NewInstaller(NewTestSource(testEndpoints(server)))
	root := t.TempDir()
	if err := installer.Install(context.Background(), root, Plan{Loader: LoaderVanilla, MinecraftVersion: "1.21.1"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, ServerJarName))
	if !bytes.Equal(got, jar) {
		t.Fatal("vanilla server.jar content mismatch")
	}
	bad := t.TempDir()
	if err := installer.Install(context.Background(), bad, Plan{Loader: LoaderVanilla, MinecraftVersion: "1.20.4"}, nil, nil); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("digest mismatch must fail, got %v", err)
	}
	if entries, _ := os.ReadDir(bad); len(entries) != 0 {
		t.Fatalf("a failed download must leave no files: %v", entries)
	}
	if err := installer.Install(context.Background(), t.TempDir(), Plan{Loader: LoaderVanilla, MinecraftVersion: "1.19.9"}, nil, nil); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("unknown version = %v", err)
	}
	fabricRoot := t.TempDir()
	if err := installer.Install(context.Background(), fabricRoot, Plan{Loader: LoaderFabric, MinecraftVersion: "1.21.1", LoaderVersion: "0.19.5"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(fabricRoot, ServerJarName)); string(data) != "fabric-launch-jar" {
		t.Fatal("fabric jar mismatch")
	}
	if err := installer.Install(context.Background(), t.TempDir(), Plan{Loader: LoaderFabric, MinecraftVersion: "1.21.1", LoaderVersion: "9.9.9"}, nil, nil); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("unoffered fabric loader = %v", err)
	}
}

func TestPlanValidation(t *testing.T) {
	cases := []struct {
		plan Plan
		ok   bool
	}{
		{Plan{Loader: LoaderVanilla, MinecraftVersion: "1.21.1"}, true},
		{Plan{Loader: LoaderVanilla, MinecraftVersion: "1.21.1", LoaderVersion: "1"}, false},
		{Plan{Loader: LoaderNeoForge, MinecraftVersion: "1.21.1", LoaderVersion: "21.1.77"}, true},
		{Plan{Loader: LoaderNeoForge, MinecraftVersion: "1.21", LoaderVersion: "21.1.77"}, false},
		{Plan{Loader: LoaderForge, MinecraftVersion: "1.20.1", LoaderVersion: "47.4.0"}, true},
		{Plan{Loader: LoaderForge, MinecraftVersion: "1.12.2", LoaderVersion: "14.23.5.2860"}, false},
		{Plan{Loader: LoaderFabric, MinecraftVersion: "1.21.1", LoaderVersion: "0.19.5"}, true},
		{Plan{Loader: LoaderFabric, MinecraftVersion: "1.21.1", LoaderVersion: "../../x"}, false},
		{Plan{Loader: LoaderFabric, MinecraftVersion: "1.21.1; rm", LoaderVersion: "0.1"}, false},
		{Plan{Loader: "quilt", MinecraftVersion: "1.21.1", LoaderVersion: "1"}, false},
	}
	for _, test := range cases {
		if err := test.plan.Validate(); (err == nil) != test.ok {
			t.Errorf("%+v: err=%v want ok=%v", test.plan, err, test.ok)
		}
	}
}

func writeFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveLaunch(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveLaunch(root, "linux", Plan{Loader: LoaderVanilla, MinecraftVersion: "1.21.1"}, 1024, 2048, true); err == nil {
		t.Fatal("missing server.jar must fail")
	}
	writeFile(t, root, "server.jar", "x")
	launch, err := ResolveLaunch(root, "linux", Plan{Loader: LoaderFabric, MinecraftVersion: "1.21.1", LoaderVersion: "0.19.5"}, 1024, 2048, true)
	if err != nil || strings.Join(launch.Arguments, " ") != "-Xms1024M -Xmx2048M -jar server.jar nogui" || launch.StopCommand != "stop" {
		t.Fatalf("fabric launch = %+v, %v", launch, err)
	}
	neo := Plan{Loader: LoaderNeoForge, MinecraftVersion: "1.21.1", LoaderVersion: "21.1.77"}
	writeFile(t, root, "libraries/net/neoforged/neoforge/21.1.77/win_args.txt", "-p libraries/a.jar --launchTarget forgeserver")
	launch, err = ResolveLaunch(root, "windows", neo, 1024, 4096, false)
	if err != nil || launch.Arguments[2] != "@libraries/net/neoforged/neoforge/21.1.77/win_args.txt" || len(launch.Arguments) != 3 {
		t.Fatalf("neoforge launch = %+v, %v", launch, err)
	}
	if _, err = ResolveLaunch(root, "linux", neo, 1024, 4096, false); err == nil {
		t.Fatal("unix argfile is absent and must fail")
	}
	forge := Plan{Loader: LoaderForge, MinecraftVersion: "1.20.1", LoaderVersion: "47.4.0"}
	writeFile(t, root, "libraries/net/minecraftforge/forge/1.20.1-47.4.0/unix_args.txt", "-javaagent:evil.jar")
	if _, err = ResolveLaunch(root, "linux", forge, 1024, 4096, true); err == nil {
		t.Fatal("javaagent argfile must be rejected")
	}
	writeFile(t, root, "libraries/net/minecraftforge/forge/1.20.1-47.4.0/unix_args.txt", "-p libraries/x.jar cpw.mods.bootstraplauncher.BootstrapLauncher")
	if launch, err = ResolveLaunch(root, "linux", forge, 1024, 4096, true); err != nil || launch.Arguments[2] != "@libraries/net/minecraftforge/forge/1.20.1-47.4.0/unix_args.txt" || launch.Arguments[3] != "nogui" {
		t.Fatalf("forge launch = %+v, %v", launch, err)
	}
	if _, err = ResolveLaunch(root, "linux", forge, 4096, 1024, true); err == nil {
		t.Fatal("inverted memory range must fail")
	}
}

func TestWriteServerPropertiesNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	if err := WriteServerProperties(root, 25570); err != nil {
		t.Fatal(err)
	}
	if err := WriteServerProperties(root, 25580); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "server.properties")); string(data) != "server-port=25570\n" {
		t.Fatalf("server.properties = %q", data)
	}
	if err := WriteServerProperties(root, 0); err == nil {
		t.Fatal("invalid port must fail")
	}
}

func buildJar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, _ := writer.Create(name)
		entry.Write([]byte(content))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestModManagerAddListRemove(t *testing.T) {
	root := t.TempDir()
	manager := NewModManager(filesystem.New())
	if mods, err := manager.List(root); err != nil || len(mods) != 0 {
		t.Fatalf("missing mods dir = %v, %v", mods, err)
	}
	fabric := buildJar(t, map[string]string{"fabric.mod.json": `{"id":"sodium","name":"Sodium","version":"0.6.0","description":"fast\u0007 renderer"}`})
	forge := buildJar(t, map[string]string{
		"META-INF/mods.toml":   "modLoader=\"javafml\"\n[[mods]]\nmodId=\"jei\"\nversion=\"${file.jarVersion}\"\ndisplayName=\"Just Enough Items\"\n[[dependencies.jei]]\nmodId=\"forge\"\n",
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\r\nImplementation-Version: 15.2.0\r\n",
	})
	neo := buildJar(t, map[string]string{"META-INF/neoforge.mods.toml": "[[mods]]\nmodId = \"create\"\nversion = \"6.0\"\ndisplayName = 'Create'\n"})
	for name, data := range map[string][]byte{"sodium.jar": fabric, "jei.jar": forge, "create-neo.JAR": neo} {
		if _, err := manager.Add(root, name, bytes.NewReader(data), false); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	mods, err := manager.List(root)
	if err != nil || len(mods) != 3 {
		t.Fatalf("list = %+v, %v", mods, err)
	}
	if mods[0].FileName != "create-neo.JAR" || mods[0].ID != "create" || mods[0].Name != "Create" || mods[0].Version != "6.0" || mods[0].Loaders[0] != "neoforge" {
		t.Fatalf("neoforge mod = %+v", mods[0])
	}
	if mods[1].ID != "jei" || mods[1].Version != "15.2.0" || mods[1].Loaders[0] != "forge" || mods[1].Name != "Just Enough Items" {
		t.Fatalf("forge mod (manifest version fallback) = %+v", mods[1])
	}
	if mods[2].ID != "sodium" || mods[2].Description != "fast  renderer" || mods[2].Loaders[0] != "fabric" {
		t.Fatalf("fabric mod (control chars stripped) = %+v", mods[2])
	}
	if _, err = manager.Add(root, "sodium.jar", bytes.NewReader(fabric), false); !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("duplicate without overwrite = %v", err)
	}
	if _, err = manager.Add(root, "sodium.jar", bytes.NewReader(fabric), true); err != nil {
		t.Fatalf("overwrite = %v", err)
	}
	for _, bad := range []string{"../evil.jar", "a/b.jar", "evil.exe", "evil.jar.exe", ".hidden.jar", "x.jar\x00"} {
		if _, err = manager.Add(root, bad, bytes.NewReader(fabric), false); !errors.Is(err, ErrInvalidModFile) {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
	if _, err = manager.Add(root, "notajar.jar", strings.NewReader("hello world, definitely not a zip"), false); !errors.Is(err, ErrNotAJar) {
		t.Fatalf("non-zip = %v", err)
	}
	if _, err = manager.Add(root, "truncated.jar", bytes.NewReader(append([]byte("PK\x03\x04"), []byte("garbage")...)), false); !errors.Is(err, ErrNotAJar) {
		t.Fatalf("truncated zip = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "mods", "truncated.jar")); statErr == nil {
		t.Fatal("an invalid archive must not remain on disk")
	}
	if err = manager.Remove(root, "jei.jar"); err != nil {
		t.Fatal(err)
	}
	if err = manager.Remove(root, "jei.jar"); !errors.Is(err, ErrModNotFound) {
		t.Fatalf("second remove = %v", err)
	}
	if err = manager.Remove(root, "../server.properties"); !errors.Is(err, ErrInvalidModFile) {
		t.Fatalf("traversal remove = %v", err)
	}
	if mods, _ = manager.List(root); len(mods) != 2 {
		t.Fatalf("after remove = %+v", mods)
	}
}

func TestNeoForgeVersionMapping(t *testing.T) {
	for version, want := range map[string]string{"21.1.77": "1.21.1", "21.0.5": "1.21", "20.2.3-beta": "1.20.2", "26.3.0.45-beta": "26.3", "26.3.1.2": "26.3.1"} {
		if got, ok := neoForgeMinecraftVersion(version); !ok || got != want {
			t.Errorf("%s => %q,%v want %q", version, got, ok, want)
		}
	}
	for _, version := range []string{"0.25w14craftmine.3-beta", "47.1.0", "abc"} {
		if _, ok := neoForgeMinecraftVersion(version); ok {
			t.Errorf("%s must not map", version)
		}
	}
}

func TestRequiredJava(t *testing.T) {
	for version, want := range map[string]int{"1.16.5": 8, "1.17.1": 16, "1.18.2": 17, "1.20.4": 17, "1.20.5": 21, "1.21.1": 21, "26.3": 25} {
		if got := RequiredJavaMajor(version); got != want {
			t.Errorf("%s => %d want %d", version, got, want)
		}
	}
}

func TestModManagerDisableEnableKeepsFileInstalled(t *testing.T) {
	root := t.TempDir()
	manager := NewModManager(filesystem.New())
	jar := buildJar(t, map[string]string{"fabric.mod.json": `{"id":"lithium","name":"Lithium","version":"0.12"}`})
	if _, err := manager.Add(root, "lithium.jar", bytes.NewReader(jar), false); err != nil {
		t.Fatal(err)
	}
	mod, err := manager.SetEnabled(root, "lithium.jar", false)
	if err != nil || mod.FileName != "lithium.jar.disabled" || !mod.Disabled || mod.ID != "lithium" || mod.Version != "0.12" {
		t.Fatalf("disable = %+v, %v", mod, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "mods", "lithium.jar")); statErr == nil {
		t.Fatal("a disabled mod must not match the loader's *.jar scan")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "mods", "lithium.jar.disabled")); !bytes.Equal(data, jar) {
		t.Fatal("a disabled mod must stay installed unchanged")
	}
	if mod, err = manager.SetEnabled(root, "lithium.jar.disabled", false); err != nil || mod.FileName != "lithium.jar.disabled" {
		t.Fatalf("disabling twice must be a no-op: %+v, %v", mod, err)
	}
	listed, _ := manager.List(root)
	if len(listed) != 1 || !listed[0].Disabled || listed[0].Name != "Lithium" {
		t.Fatalf("list = %+v", listed)
	}
	if mod, err = manager.SetEnabled(root, "lithium.jar.disabled", true); err != nil || mod.FileName != "lithium.jar" || mod.Disabled {
		t.Fatalf("enable = %+v, %v", mod, err)
	}
	if _, err = manager.SetEnabled(root, "lithium.jar", false); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Add(root, "lithium.jar", bytes.NewReader(jar), false); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.SetEnabled(root, "lithium.jar.disabled", true); !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("enabling over an existing enabled copy must conflict, got %v", err)
	}
	for _, bad := range []string{"../x.jar", "x.txt", "x.jar.disabled.disabled", "lithium.jar.exe", "a/b.jar.disabled"} {
		if _, err = manager.SetEnabled(root, bad, true); !errors.Is(err, ErrInvalidModFile) {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
	if _, err = manager.SetEnabled(root, "missing.jar", false); !errors.Is(err, ErrModNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if err = manager.Remove(root, "lithium.jar.disabled"); err != nil {
		t.Fatalf("a disabled mod can be removed: %v", err)
	}
}
