package vintagestory

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gamenode/internal/filesystem"
)

func buildArchive(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	if runtime.GOOS == "windows" {
		writer := zip.NewWriter(&buffer)
		for name, content := range files {
			entry, _ := writer.Create(name)
			entry.Write([]byte(content))
		}
		writer.Close()
		return buffer.Bytes(), "zip"
	}
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	tw.Close()
	gz.Close()
	return buffer.Bytes(), "tar.gz"
}

func archiveName(version string) string {
	if runtime.GOOS == "windows" {
		return "vs_server_win-x64_" + version + ".zip"
	}
	return "vs_server_linux-x64_" + version + ".tar.gz"
}

func testServer(t *testing.T, good map[string]string) (*httptest.Server, Endpoints) {
	t.Helper()
	archive, _ := buildArchive(t, good)
	sum := md5.Sum(archive)
	digest := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	var server *httptest.Server
	entry := func(version, md5sum, name string) string {
		file := fmt.Sprintf(`{"filename":%q,"md5":%q,"urls":{"cdn":"%s/files/%s"}}`, name, md5sum, server.URL, version)
		return fmt.Sprintf(`{"linuxserver":%s,"windowsserver":%s}`, file, file)
	}
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"1.22.7":%s,"1.22.0-rc.10":%s,"1.22.0":%s,"1.21.6":%s,"1.20.0-pre.1":%s,"1.19.0":{"linux":{}},"bad;version":%s,"1.21.0":%s}`,
			entry("1.22.7", digest, archiveName("1.22.7")),
			entry("1.22.0-rc.10", digest, archiveName("1.22.0-rc.10")),
			entry("1.22.0", digest, archiveName("1.22.0")),
			entry("1.21.6", strings.Repeat("0", 32), archiveName("1.21.6")),
			entry("1.20.0-pre.1", digest, "x"),
			entry("1.1.1", digest, "x"),
			entry("1.21.0", digest, "evil-name.tar.gz"))
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) { w.Write(archive) })
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	parsed, _ := url.Parse(server.URL)
	return server, Endpoints{VersionIndex: server.URL + "/index.json", AllowedHosts: []string{parsed.Hostname()}, AllowInsecure: true}
}

var goodServer = map[string]string{
	"VintagestoryServer.dll":                "dll",
	"VintagestoryServer.runtimeconfig.json": `{"runtimeOptions":{"framework":{"name":"Microsoft.NETCore.App","version":"10.0.0"}}}`,
	"assets/survival/blocktypes/stone.json": "{}",
}

func TestVersionsListStableAndReleaseCandidatesNewestFirst(t *testing.T) {
	_, endpoints := testServer(t, goodServer)
	versions, err := NewTestSource(endpoints).Versions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, version := range versions {
		got = append(got, version.Version+"/"+version.Channel)
	}
	want := "1.22.7/stable,1.22.0/stable,1.22.0-rc.10/rc,1.21.6/stable,1.21.0/stable"
	if strings.Join(got, ",") != want {
		t.Fatalf("versions = %v\nwant     %s", got, want)
	}
	if !versions[0].Latest || versions[1].Latest {
		t.Fatalf("only the newest stable is latest: %+v", versions[:2])
	}
}

func TestInstallVerifiesDigestExtractsAndChecksRuntime(t *testing.T) {
	_, endpoints := testServer(t, goodServer)
	installer := NewInstaller(NewTestSource(endpoints))
	installer.SetRuntimeForTest(func() (string, bool) { return "dotnet", true }, func() map[int]bool { return map[int]bool{10: true} })
	root := t.TempDir()
	var phases []string
	if err := installer.Install(context.Background(), root, Plan{Version: "1.22.7"}, nil, func(e Event) { phases = append(phases, e.Phase) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(phases, ",") != "resolving,downloading,extracting" {
		t.Fatalf("phases = %v", phases)
	}
	for _, name := range []string{"VintagestoryServer.dll", "assets/survival/blocktypes/stone.json"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".gamenode-download") {
			t.Fatal("the downloaded archive must be removed after extraction")
		}
	}
	if major, err := RequiredRuntimeMajor(root); err != nil || major != 10 {
		t.Fatalf("runtime major = %d, %v", major, err)
	}
	bad := t.TempDir()
	if err := installer.Install(context.Background(), bad, Plan{Version: "1.21.6"}, nil, nil); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("digest mismatch = %v", err)
	}
	if entries, _ := os.ReadDir(bad); len(entries) != 0 {
		t.Fatalf("a failed download must leave nothing behind: %v", entries)
	}
	for version, want := range map[string]error{"9.9.9": ErrVersionNotFound, "1.21.0": ErrSourceUnavailable, "1.22.7; rm": ErrInvalidVersion, "../x": ErrInvalidVersion, "1.22": ErrInvalidVersion} {
		if err := installer.Install(context.Background(), t.TempDir(), Plan{Version: version}, nil, nil); !errors.Is(err, want) {
			t.Errorf("%q = %v, want %v", version, err, want)
		}
	}
	installer.SetRuntimeForTest(func() (string, bool) { return "dotnet", true }, func() map[int]bool { return map[int]bool{8: true} })
	if err := installer.Install(context.Background(), t.TempDir(), Plan{Version: "1.22.7"}, nil, nil); !errors.Is(err, ErrRuntimeMissing) {
		t.Fatalf("missing .NET 10 = %v", err)
	}
}

func TestInstallRejectsUnsafeArchiveAndIncompleteServer(t *testing.T) {
	_, endpoints := testServer(t, map[string]string{"../escape.txt": "x", "VintagestoryServer.dll": "d"})
	installer := NewInstaller(NewTestSource(endpoints))
	root := t.TempDir()
	if err := installer.Install(context.Background(), root, Plan{Version: "1.22.7"}, nil, nil); !errors.Is(err, ErrExtractFailed) {
		t.Fatalf("traversal archive = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
		t.Fatal("an archive entry escaped the server root")
	}
	_, endpoints = testServer(t, map[string]string{"readme.txt": "x"})
	if err := NewInstaller(NewTestSource(endpoints)).Install(context.Background(), t.TempDir(), Plan{Version: "1.22.7"}, nil, nil); !errors.Is(err, ErrIncompleteServer) {
		t.Fatalf("archive without the server assembly = %v", err)
	}
}

func TestSourceRejectsDisallowedHostAndPlainHTTP(t *testing.T) {
	_, endpoints := testServer(t, goodServer)
	endpoints.AllowedHosts = []string{"example.invalid"}
	if _, err := NewTestSource(endpoints).Versions(context.Background()); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("disallowed host = %v", err)
	}
	_, endpoints = testServer(t, goodServer)
	endpoints.AllowInsecure = false
	if _, err := NewTestSource(endpoints).Versions(context.Background()); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("plain HTTP = %v", err)
	}
}

func TestResolveLaunchIsStructuredAndRootRelative(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveLaunch(root, 42420); err == nil {
		t.Fatal("a missing server assembly must fail")
	}
	if err := os.WriteFile(filepath.Join(root, ServerDLL), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	launch, err := ResolveLaunch(root, 42500)
	if err != nil || strings.Join(launch.Arguments, " ") != "VintagestoryServer.dll --dataPath data --port 42500" || launch.StopCommand != "/stop" || launch.StopMethod != "stdin_command" {
		t.Fatalf("launch = %+v, %v", launch, err)
	}
	for _, argument := range launch.Arguments {
		if filepath.IsAbs(argument) {
			t.Fatalf("argument %q must stay root-relative so a tenant migration cannot break it", argument)
		}
	}
	if _, err = ResolveLaunch(root, 0); err == nil {
		t.Fatal("an invalid port must fail")
	}
}

func TestVersionHelpers(t *testing.T) {
	for version, ok := range map[string]bool{"1.22.7": true, "1.22.0-rc.10": true, "1.20.0-pre.1": false, "1.22": false, "../1.0.0": false, "1.2.3-rc.": false} {
		if ValidVersion(version) != ok {
			t.Errorf("ValidVersion(%q) = %v", version, !ok)
		}
	}
	for version, want := range map[string]int{"1.22.7": 10, "1.21.6": 8, "1.20.4": 7} {
		if got := RequiredDotnetMajor(version); got != want {
			t.Errorf("RequiredDotnetMajor(%s) = %d want %d", version, got, want)
		}
	}
	if compareVersions("1.22.0-rc.2", "1.22.0") >= 0 || compareVersions("1.22.0-rc.10", "1.22.0-rc.9") <= 0 || compareVersions("1.9.0", "1.10.0") >= 0 {
		t.Fatal("version ordering is wrong")
	}
}

func TestModProfileReadsLenientModInfo(t *testing.T) {
	root := t.TempDir()
	manager := NewModManager(filesystem.New())
	archive := func(files map[string]string) []byte {
		var buffer bytes.Buffer
		writer := zip.NewWriter(&buffer)
		for name, content := range files {
			entry, _ := writer.Create(name)
			entry.Write([]byte(content))
		}
		writer.Close()
		return buffer.Bytes()
	}
	lenient := strings.Join([]string{"{", "  // comment", "  type: \"code\",", "  \"name\": \"Primitive Survival\",", "  modID: \"primitivesurvival\",", "  version: \"3.8.1\",", "  \"description\": \"More to do.\",", "}"}, "\n")
	mod, err := manager.Add(root, "primitivesurvival_3.8.1.zip", bytes.NewReader(archive(map[string]string{"modinfo.json": lenient})), false)
	if err != nil || mod.ID != "primitivesurvival" || mod.Name != "Primitive Survival" || mod.Version != "3.8.1" || mod.Description != "More to do." {
		t.Fatalf("mod = %+v, %v", mod, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "data", "Mods", "primitivesurvival_3.8.1.zip")); statErr != nil {
		t.Fatalf("mods must live in <data>/Mods: %v", statErr)
	}
	if _, err = manager.Add(root, "tool.jar", bytes.NewReader(archive(nil)), false); err == nil {
		t.Fatal("only .zip archives are Vintage Story mods")
	}
	disabled, err := manager.SetEnabled(root, "primitivesurvival_3.8.1.zip", false)
	if err != nil || disabled.FileName != "primitivesurvival_3.8.1.zip.disabled" || !disabled.Disabled || disabled.ID != "primitivesurvival" {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}
}
