package selfupdate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gamenode/internal/selfupdate"
)

// These tests drive REAL processes: they compile a tiny stand-in application
// (testdata/fakeapp) that embeds the real selfupdate.Service, then let one
// build download, verify (real --version self-test), install (real executable
// swap while running), and relaunch (real exec / detached spawn) into another
// build, including the automatic rollback of a build that never becomes
// healthy. They cover the OS-specific parts the in-process tests cannot.

type e2eEnv struct {
	t         *testing.T
	root      string
	app       string
	data      string
	release   string
	exe       string
	assetName string
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end update test builds real binaries")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	asset, ok := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Skipf("no release asset for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	root := t.TempDir()
	env := &e2eEnv{t: t, root: root, app: filepath.Join(root, "app"), data: filepath.Join(root, "data"), release: filepath.Join(root, "release"), assetName: asset}
	for _, dir := range []string{env.app, env.data, env.release} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env.exe = filepath.Join(env.app, "gamenode"+filepath.Ext(asset))
	return env
}

func (e *e2eEnv) build(out, version string, crash bool) {
	e.t.Helper()
	flags := "-X main.version=" + version
	if crash {
		flags += " -X main.crashOnStart=1"
	}
	cmd := exec.Command("go", "build", "-ldflags", flags, "-o", out, "./testdata/fakeapp")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("build fakeapp %s: %v\n%s", version, err, output)
	}
}

// publish writes a fake release whose binary is the given build.
func (e *e2eEnv) publish(binaryPath, version string) {
	e.t.Helper()
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		e.t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(e.release, e.assetName), data, 0o644); err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	manifest := []byte(hex.EncodeToString(sum[:]) + "  " + e.assetName + "\n")
	if err = os.WriteFile(filepath.Join(e.release, selfupdate.ChecksumAsset), manifest, 0o644); err != nil {
		e.t.Fatal(err)
	}
	release := selfupdate.Release{Tag: "v" + version, Version: version, URL: "https://example.invalid", Assets: []selfupdate.Asset{{Name: e.assetName, Size: int64(len(data))}, {Name: selfupdate.ChecksumAsset, Size: int64(len(manifest))}}}
	encoded, _ := json.Marshal(release)
	if err = os.WriteFile(filepath.Join(e.release, "release.json"), encoded, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *e2eEnv) command(args ...string) *exec.Cmd {
	cmd := exec.Command(e.exe, args...)
	cmd.Dir = e.app
	cmd.Env = append(os.Environ(), "FAKEAPP_DATA="+e.data, "FAKEAPP_RELEASE="+e.release, "FAKEAPP_ACTION=update")
	return cmd
}

func (e *e2eEnv) events() string {
	data, _ := os.ReadFile(filepath.Join(e.data, "events.log"))
	return string(data)
}

func (e *e2eEnv) waitFor(substr string, timeout time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(e.events(), substr) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %q; events so far:\n%s", substr, e.events())
}

func versionOf(t *testing.T, path string) string {
	t.Helper()
	output, err := exec.Command(path, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", path, err)
	}
	return strings.TrimSpace(string(output))
}

// settle lets detached successor processes finish so the temp directory can be
// removed (Windows cannot delete a running executable).
func settle() { time.Sleep(750 * time.Millisecond) }

func TestEndToEndUpdateInstallsRelaunchesAndConfirms(t *testing.T) {
	e := newE2EEnv(t)
	e.build(e.exe, "1.0.0", false)
	newBuild := filepath.Join(e.root, "new-build"+filepath.Ext(e.exe))
	e.build(newBuild, "2.0.0", false)
	e.publish(newBuild, "2.0.0")

	first := e.command()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = first.Wait() }()

	e.waitFor("outcome completed 1.0.0->2.0.0 actor=admin", 60*time.Second)
	e.waitFor("exit 2.0.0", 30*time.Second)
	log := e.events()
	ordered := []string{"start 1.0.0", "applied 2.0.0", "start 2.0.0", "outcome completed"}
	last := -1
	for _, want := range ordered {
		index := strings.Index(log, want)
		if index <= last {
			t.Fatalf("event %q missing or out of order in:\n%s", want, log)
		}
		last = index
	}
	if strings.Count(log, "start ") != 2 {
		t.Fatalf("expected exactly two starts (old, then relaunched new):\n%s", log)
	}
	settle()
	if got := versionOf(t, e.exe); got != "gamenode 2.0.0" {
		t.Fatalf("installed executable reports %q", got)
	}
	if got := versionOf(t, selfupdate.BackupPath(e.exe)); got != "gamenode 1.0.0" {
		t.Fatalf("the previous version must be kept for rollback, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.data, "updates", "pending.json")); !os.IsNotExist(err) {
		t.Fatal("marker must be cleared once the new version is confirmed healthy")
	}
	if _, err := os.Stat(selfupdate.StagedPath(e.exe)); !os.IsNotExist(err) {
		t.Fatal("no staged file may remain after a completed update")
	}
	backups, _ := filepath.Glob(filepath.Join(e.data, "updates", "backups", "pre-update-1.0.0-to-2.0.0-*.db"))
	if len(backups) != 1 {
		t.Fatalf("expected one pre-update database backup, got %v", backups)
	}
}

func TestEndToEndUnhealthyUpdateRollsBackAutomatically(t *testing.T) {
	e := newE2EEnv(t)
	e.build(e.exe, "1.0.0", false)
	badBuild := filepath.Join(e.root, "bad-build"+filepath.Ext(e.exe))
	e.build(badBuild, "2.0.0", true) // reports the right version but crashes on every start
	e.publish(badBuild, "2.0.0")

	first := e.command()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = first.Wait() }()
	// The self-test (--version) passes, so the bad build is installed and
	// relaunched; it then crashes right after Boot.
	e.waitFor("applied 2.0.0", 60*time.Second)
	e.waitFor("start 2.0.0", 30*time.Second)
	time.Sleep(500 * time.Millisecond)

	// Act as the supervisor (systemd / a service wrapper): keep restarting the
	// crashing installation until the updater restores the previous version.
	for attempt := 0; attempt < 6 && !strings.Contains(e.events(), "outcome rolled_back"); attempt++ {
		cmd := e.command()
		if err := cmd.Run(); err != nil {
			// The crashing build exits non-zero; that is expected.
			_ = err
		}
		time.Sleep(400 * time.Millisecond)
	}
	e.waitFor("outcome rolled_back 1.0.0->2.0.0 actor=admin", 30*time.Second)
	e.waitFor("exit 1.0.0", 30*time.Second)
	log := e.events()
	if !strings.Contains(log, "rollback-relaunch from=2.0.0") {
		t.Fatalf("the unhealthy build must hand over to the restored one:\n%s", log)
	}
	if got := strings.Count(log, "start 2.0.0"); got != 2 {
		t.Fatalf("the bad build should be tolerated for exactly two unconfirmed starts, saw %d:\n%s", got, log)
	}
	settle()
	if got := versionOf(t, e.exe); got != "gamenode 1.0.0" {
		t.Fatalf("the previous version must be restored, executable reports %q", got)
	}
	failed, _ := filepath.Glob(filepath.Join(e.app, "gamenode.failed*"))
	if len(failed) != 1 {
		t.Fatalf("the failed binary should be kept aside for diagnosis, got %v", failed)
	}
	if _, err := os.Stat(filepath.Join(e.data, "updates", "pending.json")); !os.IsNotExist(err) {
		t.Fatal("marker must be cleared after the rollback has been reported")
	}
}

func TestEndToEndRejectsCorruptedRelease(t *testing.T) {
	e := newE2EEnv(t)
	e.build(e.exe, "1.0.0", false)
	newBuild := filepath.Join(e.root, "new-build"+filepath.Ext(e.exe))
	e.build(newBuild, "2.0.0", false)
	e.publish(newBuild, "2.0.0")
	// Corrupt the published binary after the checksum manifest was written.
	path := filepath.Join(e.release, e.assetName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err = os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := e.command()
	if err = cmd.Run(); err == nil {
		t.Fatal("the app exits non-zero when the download fails verification")
	}
	if !strings.Contains(e.events(), "download-failed") || !strings.Contains(e.events(), selfupdate.CodeChecksumMismatch) {
		t.Fatalf("expected a checksum mismatch, events:\n%s", e.events())
	}
	settle()
	if got := versionOf(t, e.exe); got != "gamenode 1.0.0" {
		t.Fatalf("a corrupted release must never replace the running version, got %q", got)
	}
	if _, err = os.Stat(selfupdate.StagedPath(e.exe)); !os.IsNotExist(err) {
		t.Fatal("a rejected download must not be left on disk")
	}
	if _, err = os.Stat(selfupdate.BackupPath(e.exe)); !os.IsNotExist(err) {
		t.Fatal("nothing was installed, so no backup may exist")
	}
}
