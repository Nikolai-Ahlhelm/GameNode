package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeELF builds a minimal but plausible linux/amd64 ELF image above the
// minimum size, with distinguishing trailing content.
func fakeELF(marker string) []byte {
	data := make([]byte, minBinaryBytes+64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	data[18], data[19] = 0x3E, 0
	copy(data[minBinaryBytes:], marker)
	return data
}

type fakeSource struct {
	mu        sync.Mutex
	release   Release
	latestErr error
	files     map[string][]byte
	opens     []string
	gate      chan struct{} // when set, binary downloads block until it is closed
}

func (f *fakeSource) Latest(context.Context) (Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.release, f.latestErr
}

func (f *fakeSource) Open(ctx context.Context, release Release, asset string, maxBytes int64) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	f.opens = append(f.opens, asset)
	data, ok := f.files[asset]
	gate := f.gate
	f.mu.Unlock()
	if !ok {
		return nil, 0, errUnavailable
	}
	if int64(len(data)) > maxBytes {
		return nil, 0, errTooLarge
	}
	if gate != nil && asset != ChecksumAsset {
		return io.NopCloser(&gatedReader{ctx: ctx, gate: gate, data: bytes.NewReader(data)}), int64(len(data)), nil
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

type gatedReader struct {
	ctx  context.Context
	gate chan struct{}
	data *bytes.Reader
}

func (g *gatedReader) Read(p []byte) (int, error) {
	select {
	case <-g.gate:
	case <-g.ctx.Done():
		return 0, g.ctx.Err()
	}
	return g.data.Read(p)
}

type harness struct {
	t        *testing.T
	dir      string
	exe      string
	data     string
	source   *fakeSource
	binary   []byte
	activity Activity
	dbErr    error
	free     uint64
	selfTest func(context.Context, string) (string, error)
	backups  []string
	svc      *Service
}

func newHarness(t *testing.T, current, latest string) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, dir: dir, exe: filepath.Join(dir, "gamenode"), data: filepath.Join(dir, "data"), free: 10 << 30}
	if err := os.MkdirAll(h.data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.binary = fakeELF("new-binary")
	sum := sha256.Sum256(h.binary)
	manifest := hex.EncodeToString(sum[:]) + "  gamenode-linux-amd64\n"
	h.source = &fakeSource{
		release: Release{Tag: "v" + latest, Version: latest, URL: "https://example.invalid/release", Assets: []Asset{{Name: "gamenode-linux-amd64", Size: int64(len(h.binary))}, {Name: ChecksumAsset, Size: int64(len(manifest))}}},
		files:   map[string][]byte{"gamenode-linux-amd64": h.binary, ChecksumAsset: []byte(manifest)},
	}
	h.selfTest = func(_ context.Context, _ string) (string, error) { return latest, nil }
	h.build(current)
	return h
}

func (h *harness) build(current string) {
	h.t.Helper()
	svc, err := New(Options{
		DataDirectory: h.data, CurrentVersion: current, Source: h.source, Executable: h.exe, GOOS: "linux", GOARCH: "amd64",
		Activity:      func(context.Context) (Activity, error) { return h.activity, nil },
		DatabaseCheck: func(context.Context) error { return h.dbErr },
		DatabaseBackup: func(_ context.Context, path string) error {
			h.backups = append(h.backups, path)
			return os.WriteFile(path, []byte("db"), 0o600)
		},
		FreeBytes:    func(string) (uint64, error) { return h.free, nil },
		SelfTest:     func(ctx context.Context, path string) (string, error) { return h.selfTest(ctx, path) },
		RestartDelay: time.Millisecond, ConfirmAfter: 20 * time.Millisecond,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.svc = svc
}

func (h *harness) waitState(want State) Status {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := h.svc.Status(context.Background())
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for state %s; at %s (error %+v)", want, status.State, status.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) stage() {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.svc.Check(ctx); err != nil {
		h.t.Fatal(err)
	}
	if err := h.svc.Prepare(ctx, h.source.release.Version); err != nil {
		h.t.Fatalf("prepare: %v", err)
	}
	h.waitState(StateReady)
}

func checkByID(checks []Check, id string) (Check, bool) {
	for _, check := range checks {
		if check.ID == id {
			return check, true
		}
	}
	return Check{}, false
}

func errorCode(err error) string {
	var updateErr *Error
	if errors.As(err, &updateErr) {
		return updateErr.Code
	}
	return ""
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 32 {
		return string(data[minBinaryBytes:])[:10]
	}
	return string(data)
}

func TestFullUpdateLifecycle(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	ctx := context.Background()
	status, err := h.svc.Check(ctx)
	if err != nil || !status.UpdateAvailable || status.Available == nil || status.Available.Version != "1.1.0" {
		t.Fatalf("check: %+v %v", status, err)
	}
	if !status.CanPrepare || status.CanApply {
		t.Fatalf("before download: prepare=%v apply=%v checks=%+v", status.CanPrepare, status.CanApply, status.Checks)
	}
	h.stage()
	status = h.svc.Status(ctx)
	if status.Staged == nil || status.Staged.Version != "1.1.0" || !status.CanApply {
		t.Fatalf("after download: %+v", status)
	}
	if got := readFile(t, h.exe); got != "old-binary" {
		t.Fatalf("download must not touch the installed executable, got %q", got)
	}
	if _, err := os.Stat(StagedPath(h.exe)); err != nil {
		t.Fatalf("staged file missing: %v", err)
	}
	if err := h.svc.Apply(ctx, "1.1.0", false, Actor{ID: "u1", Username: "admin"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := readFile(t, h.exe); got != "new-binary" {
		t.Fatalf("new binary not installed: %q", got)
	}
	if got := readFile(t, BackupPath(h.exe)); got != "old-binary" {
		t.Fatalf("previous binary must be kept as the backup: %q", got)
	}
	if len(h.backups) != 1 || !strings.Contains(h.backups[0], "pre-update-1.0.0-to-1.1.0-") {
		t.Fatalf("database must be backed up before the swap: %v", h.backups)
	}
	m, present, err := readMarker(h.data)
	if err != nil || !present || m.State != markerApplied || m.From != "1.0.0" || m.To != "1.1.0" || m.ActorUsername != "admin" {
		t.Fatalf("marker: %+v present=%v err=%v", m, present, err)
	}
	select {
	case <-h.svc.RestartRequested():
	case <-time.After(3 * time.Second):
		t.Fatal("restart was never requested")
	}
	if !h.svc.RestartPending() {
		t.Fatal("RestartPending must be true once restart is requested")
	}
	if status = h.svc.Status(ctx); status.State != StateRestarting {
		t.Fatalf("state after apply: %s", status.State)
	}
	if err := h.svc.Apply(ctx, "1.1.0", false, Actor{}); errorCode(err) != CodeBusy {
		t.Fatalf("second apply must be refused as busy, got %v", err)
	}
}

func TestDevelopmentBuildCannotUpdate(t *testing.T) {
	h := newHarness(t, "dev", "1.1.0")
	status, _ := h.svc.Check(context.Background())
	if status.Updatable || status.UpdateAvailable || status.CanPrepare {
		t.Fatalf("dev build must not be updatable: %+v", status)
	}
	err := h.svc.Prepare(context.Background(), "1.1.0")
	if errorCode(err) != CodePreflightBlocked {
		t.Fatalf("prepare on dev build: %v", err)
	}
}

func TestDowngradeAndSameVersionAreNotOffered(t *testing.T) {
	for _, latest := range []string{"1.0.0", "0.9.0"} {
		h := newHarness(t, "v1.0.0", latest)
		status, _ := h.svc.Check(context.Background())
		if status.UpdateAvailable || status.CanPrepare {
			t.Fatalf("latest %s: %+v", latest, status)
		}
		if err := h.svc.Prepare(context.Background(), latest); errorCode(err) != CodePreflightBlocked {
			t.Fatalf("latest %s: prepare should be blocked, got %v", latest, err)
		}
	}
	h := newHarness(t, "v1.0.0-rc.1", "1.0.0")
	if status, _ := h.svc.Check(context.Background()); !status.UpdateAvailable {
		t.Fatal("a final release must be offered over its own release candidate")
	}
}

func TestPrepareRequiresTheCheckedRelease(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	if err := h.svc.Prepare(context.Background(), "1.1.0"); errorCode(err) != CodeReleaseUnknown {
		t.Fatalf("prepare before check: %v", err)
	}
	_, _ = h.svc.Check(context.Background())
	if err := h.svc.Prepare(context.Background(), "9.9.9"); errorCode(err) != CodeReleaseUnknown {
		t.Fatalf("prepare of an unchecked version: %v", err)
	}
	if err := h.svc.Prepare(context.Background(), "not-a-version"); errorCode(err) != CodeReleaseUnknown {
		t.Fatalf("prepare of a malformed version: %v", err)
	}
	if len(h.source.opens) != 0 {
		t.Fatalf("nothing may be downloaded for a rejected request: %v", h.source.opens)
	}
}

func TestChecksumMismatchDiscardsDownload(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	tampered := append([]byte(nil), h.binary...)
	tampered[minBinaryBytes+1] ^= 0xff
	h.source.files["gamenode-linux-amd64"] = tampered
	_, _ = h.svc.Check(context.Background())
	if err := h.svc.Prepare(context.Background(), "1.1.0"); err != nil {
		t.Fatal(err)
	}
	status := h.waitState(StateFailed)
	if status.Error == nil || status.Error.Code != CodeChecksumMismatch {
		t.Fatalf("expected checksum mismatch, got %+v", status.Error)
	}
	if _, err := os.Stat(StagedPath(h.exe)); !os.IsNotExist(err) {
		t.Fatal("a download that fails verification must not remain on disk")
	}
	if status.CanApply || status.Staged != nil {
		t.Fatal("a failed download must not be installable")
	}
	if got := readFile(t, h.exe); got != "old-binary" {
		t.Fatal("installed executable changed")
	}
}

func TestMissingOrMalformedManifestFailsClosed(t *testing.T) {
	for name, mutate := range map[string]func(h *harness){
		"no entry": func(h *harness) {
			h.source.files[ChecksumAsset] = []byte(strings.Repeat("ab", 32) + "  something-else\n")
		},
		"malformed": func(h *harness) { h.source.files[ChecksumAsset] = []byte("garbage") },
		"absent":    func(h *harness) { delete(h.source.files, ChecksumAsset) },
	} {
		h := newHarness(t, "v1.0.0", "1.1.0")
		mutate(h)
		_, _ = h.svc.Check(context.Background())
		if err := h.svc.Prepare(context.Background(), "1.1.0"); err != nil {
			t.Fatal(err)
		}
		status := h.waitState(StateFailed)
		if status.Error == nil || status.Error.Code != CodeChecksumUnavail {
			t.Errorf("%s: %+v", name, status.Error)
		}
		for _, asset := range h.source.opens {
			if asset != ChecksumAsset {
				t.Errorf("%s: the binary must not be downloaded before its checksum is known", name)
			}
		}
	}
}

func TestSizeMismatchAndNonExecutableAndSelfTestAreRejected(t *testing.T) {
	cases := map[string]struct {
		mutate func(h *harness)
		code   string
	}{
		"size": {func(h *harness) {
			h.source.release.Assets[0].Size++
		}, CodeSizeMismatch},
		"not executable": {func(h *harness) {
			junk := bytes.Repeat([]byte("x"), minBinaryBytes+10)
			sum := sha256.Sum256(junk)
			h.source.files["gamenode-linux-amd64"] = junk
			h.source.files[ChecksumAsset] = []byte(hex.EncodeToString(sum[:]) + "  gamenode-linux-amd64\n")
			h.source.release.Assets[0].Size = int64(len(junk))
		}, CodeNotExecutable},
		"self test error": {func(h *harness) {
			h.selfTest = func(context.Context, string) (string, error) { return "", errors.New("boom") }
		}, CodeSelfTestFailed},
		"self test wrong version": {func(h *harness) {
			h.selfTest = func(context.Context, string) (string, error) { return "9.9.9", nil }
		}, CodeSelfTestFailed},
	}
	for name, tc := range cases {
		h := newHarness(t, "v1.0.0", "1.1.0")
		tc.mutate(h)
		_, _ = h.svc.Check(context.Background())
		if err := h.svc.Prepare(context.Background(), "1.1.0"); err != nil {
			t.Fatal(err)
		}
		status := h.waitState(StateFailed)
		if status.Error == nil || status.Error.Code != tc.code {
			t.Errorf("%s: got %+v want %s", name, status.Error, tc.code)
		}
		if _, err := os.Stat(StagedPath(h.exe)); !os.IsNotExist(err) {
			t.Errorf("%s: rejected download left on disk", name)
		}
	}
}

func TestBlockingChecksPreventPrepareAndApply(t *testing.T) {
	cases := map[string]struct {
		set func(h *harness)
		id  string
	}{
		"provisioning job":   {func(h *harness) { h.activity.ProvisioningJobs = 1 }, "active_jobs"},
		"server update job":  {func(h *harness) { h.activity.ServerUpdateJobs = 2 }, "active_jobs"},
		"server transition":  {func(h *harness) { h.activity.TransitionalServers = 1 }, "server_transitions"},
		"database unhealthy": {func(h *harness) { h.dbErr = errors.New("corrupt") }, "database"},
		"no disk space":      {func(h *harness) { h.free = 1 << 20 }, "disk_space"},
	}
	for name, tc := range cases {
		// Blocked before download.
		h := newHarness(t, "v1.0.0", "1.1.0")
		tc.set(h)
		_, _ = h.svc.Check(context.Background())
		err := h.svc.Prepare(context.Background(), "1.1.0")
		var updateErr *Error
		if !errors.As(err, &updateErr) || updateErr.Code != CodePreflightBlocked {
			t.Fatalf("%s: prepare must be blocked, got %v", name, err)
		}
		if check, ok := checkByID(updateErr.Checks, tc.id); !ok || check.Status != CheckBlock {
			t.Errorf("%s: expected blocking check %s in %+v", name, tc.id, updateErr.Checks)
		}
		// Becomes blocked after download, before install: re-checked at apply time.
		h = newHarness(t, "v1.0.0", "1.1.0")
		h.stage()
		tc.set(h)
		h.svc.mu.Lock()
		h.svc.invalidateCacheLocked()
		h.svc.mu.Unlock()
		if status := h.svc.Status(context.Background()); status.CanApply {
			t.Errorf("%s: status must report apply as blocked", name)
		}
		err = h.svc.Apply(context.Background(), "1.1.0", true, Actor{})
		if !errors.As(err, &updateErr) || updateErr.Code != CodePreflightBlocked {
			t.Fatalf("%s: apply must be blocked even when acknowledged, got %v", name, err)
		}
		if got := readFile(t, h.exe); got != "old-binary" {
			t.Errorf("%s: a blocked apply must not touch the executable", name)
		}
		if _, present, _ := readMarker(h.data); present {
			t.Errorf("%s: a blocked apply must not write a marker", name)
		}
		if status := h.svc.Status(context.Background()); status.State != StateReady || status.Staged == nil {
			t.Errorf("%s: a blocked apply must leave the verified download staged, got %s", name, status.State)
		}
	}
}

func TestRunningServersRequireAcknowledgement(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	h.activity.RunningServers = 3
	h.stage()
	status := h.svc.Status(context.Background())
	if !status.RequiresAck || !status.CanApply {
		t.Fatalf("running servers should warn, not block: %+v", status.Checks)
	}
	err := h.svc.Apply(context.Background(), "1.1.0", false, Actor{})
	var updateErr *Error
	if !errors.As(err, &updateErr) || updateErr.Code != CodeAcknowledge {
		t.Fatalf("unacknowledged apply: %v", err)
	}
	if got := readFile(t, h.exe); got != "old-binary" {
		t.Fatal("unacknowledged apply changed the executable")
	}
	if err := h.svc.Apply(context.Background(), "1.1.0", true, Actor{}); err != nil {
		t.Fatalf("acknowledged apply: %v", err)
	}
}

func TestApplyDetectsStagedFileTampering(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	h.stage()
	if err := os.WriteFile(StagedPath(h.exe), fakeELF("evil-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := h.svc.Apply(context.Background(), "1.1.0", true, Actor{})
	if errorCode(err) != CodeStagedTampered {
		t.Fatalf("expected tamper detection, got %v", err)
	}
	if got := readFile(t, h.exe); got != "old-binary" {
		t.Fatal("tampered binary was installed")
	}
	if _, err := os.Stat(StagedPath(h.exe)); !os.IsNotExist(err) {
		t.Fatal("tampered staged file must be discarded")
	}
	if status := h.svc.Status(context.Background()); status.CanApply || status.State != StateFailed {
		t.Fatalf("state after tamper: %+v", status)
	}
}

func TestBackupFailureAbortsBeforeSwap(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	h.svc.opts.DatabaseBackup = func(context.Context, string) error { return errors.New("disk full") }
	h.stage()
	err := h.svc.Apply(context.Background(), "1.1.0", true, Actor{})
	if errorCode(err) != CodeBackupFailed {
		t.Fatalf("expected backup failure, got %v", err)
	}
	if got := readFile(t, h.exe); got != "old-binary" {
		t.Fatal("executable must be untouched when the backup fails")
	}
	if status := h.svc.Status(context.Background()); status.State != StateReady {
		t.Fatalf("a retryable failure should keep the download staged: %s", status.State)
	}
}

func TestApplyWithoutDownloadIsRefused(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	_, _ = h.svc.Check(context.Background())
	if err := h.svc.Apply(context.Background(), "1.1.0", true, Actor{}); errorCode(err) != CodeNotReady {
		t.Fatalf("got %v", err)
	}
}

func TestCancelDuringDownloadLeavesNothingBehind(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	h.source.gate = make(chan struct{})
	_, _ = h.svc.Check(context.Background())
	if err := h.svc.Prepare(context.Background(), "1.1.0"); err != nil {
		t.Fatal(err)
	}
	h.waitState(StateDownloading)
	if err := h.svc.Prepare(context.Background(), "1.1.0"); errorCode(err) != CodeBusy {
		t.Fatalf("concurrent prepare must be busy, got %v", err)
	}
	if err := h.svc.Cancel(); err != nil {
		t.Fatal(err)
	}
	status := h.waitState(StateIdle)
	if status.Error != nil {
		t.Fatalf("a cancelled download is not an error: %+v", status.Error)
	}
	if _, err := os.Stat(StagedPath(h.exe)); !os.IsNotExist(err) {
		t.Fatal("cancelled download left a staged file")
	}
}

func TestCancelDiscardsStagedBinary(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	h.stage()
	if err := h.svc.Cancel(); err != nil {
		t.Fatal(err)
	}
	if status := h.svc.Status(context.Background()); status.State != StateIdle || status.Staged != nil {
		t.Fatalf("%+v", status)
	}
	if _, err := os.Stat(StagedPath(h.exe)); !os.IsNotExist(err) {
		t.Fatal("staged file must be removed")
	}
}

func TestNewerReleaseInvalidatesStagedDownload(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	h.stage()
	h.source.mu.Lock()
	h.source.release.Version, h.source.release.Tag = "1.2.0", "v1.2.0"
	h.source.mu.Unlock()
	h.svc.mu.Lock()
	h.svc.lastChecked = time.Time{} // bypass the manual-check throttle
	h.svc.mu.Unlock()
	if _, err := h.svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := h.svc.Status(context.Background())
	if status.Staged != nil || status.CanApply {
		t.Fatalf("staged binary for an older release must be discarded: %+v", status)
	}
	if err := h.svc.Apply(context.Background(), "1.1.0", true, Actor{}); errorCode(err) != CodeNotReady {
		t.Fatalf("got %v", err)
	}
}

func TestSourceFailureIsRecordedNotFatal(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	h.source.latestErr = errUnavailable
	status, err := h.svc.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.LastCheckError == nil || status.LastCheckError.Code != "source_unavailable" || status.UpdateAvailable {
		t.Fatalf("%+v", status)
	}
	if status.State != StateIdle {
		t.Fatalf("a failed check must leave the service idle: %s", status.State)
	}
	// A later successful check clears the error.
	h.source.latestErr = nil
	status, _ = h.svc.Check(context.Background())
	if status.LastCheckError != nil || !status.UpdateAvailable {
		t.Fatalf("%+v", status)
	}
}

func TestManualChecksAreThrottled(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	calls := 0
	counting := &countingSource{fakeSource: h.source, calls: &calls}
	h.svc.opts.Source = counting
	_, _ = h.svc.Check(context.Background())
	_, _ = h.svc.Check(context.Background())
	_, _ = h.svc.Check(context.Background())
	if calls != 1 {
		t.Fatalf("expected 1 upstream call within the throttle window, got %d", calls)
	}
}

type countingSource struct {
	*fakeSource
	calls *int
}

func (c *countingSource) Latest(ctx context.Context) (Release, error) {
	*c.calls++
	return c.fakeSource.Latest(ctx)
}

// A name-ordered prune would treat "1.10.0" as older than "1.9.0" and delete
// the most recent backup. Modification time is what decides.
func TestBackupPruningOrdersByTimeNotByVersionText(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	// Oldest to newest by real time; lexicographically the last one sorts FIRST.
	chronological := []string{"pre-update-1.8.0-to-1.9.0.db", "pre-update-1.9.0-to-1.10.0.db", "pre-update-1.10.0-to-1.11.0.db", "pre-update-1.11.0-to-2.0.0.db"}
	for i, name := range chronological {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, base.Add(time.Duration(i)*time.Minute), base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	pruneBackups(dir, 3)
	if _, err := os.Stat(filepath.Join(dir, chronological[0])); !os.IsNotExist(err) {
		t.Fatal("the genuinely oldest backup should have been pruned")
	}
	for _, name := range chronological[1:] {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("recent backup %s was wrongly pruned: %v", name, err)
		}
	}
}

func TestBackupsArePruned(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"pre-update-a-1.db", "pre-update-a-2.db", "pre-update-a-3.db", "pre-update-a-4.db", "unrelated.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneBackups(dir, 3)
	entries, _ := os.ReadDir(dir)
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if names["pre-update-a-1.db"] || !names["pre-update-a-4.db"] || !names["unrelated.db"] || len(names) != 4 {
		t.Fatalf("unexpected pruning result: %v", names)
	}
}

// --- boot / rollback / confirmation -----------------------------------------

func installedAsUpdated(t *testing.T, h *harness) {
	t.Helper()
	h.stage()
	if err := h.svc.Apply(context.Background(), "1.1.0", true, Actor{ID: "u1", Username: "admin"}); err != nil {
		t.Fatal(err)
	}
}

func TestBootRollsBackAnUpdateThatNeverBecomesHealthy(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	installedAsUpdated(t, h)
	// Simulate the freshly installed binary starting repeatedly and crashing
	// before it reaches health confirmation.
	for boot := 1; boot <= maxUnconfirmedBoots; boot++ {
		h.build("1.1.0")
		if result := h.svc.Boot(); result.Relaunch {
			t.Fatalf("boot %d must not roll back yet", boot)
		}
	}
	h.build("1.1.0")
	result := h.svc.Boot()
	if !result.Relaunch {
		t.Fatal("the next unconfirmed boot must restore the previous binary")
	}
	if got := readFile(t, h.exe); got != "old-binary" {
		t.Fatalf("previous binary not restored: %q", got)
	}
	if got := readFile(t, failedPath(h.exe)); got != "new-binary" {
		t.Fatalf("the failed binary should be kept aside for diagnosis: %q", got)
	}
	m, present, _ := readMarker(h.data)
	if !present || m.State != markerRolledBack || m.Reason == "" {
		t.Fatalf("marker after rollback: %+v", m)
	}

	// The restored (old) binary starts, reports the rollback exactly once.
	h.build("v1.0.0")
	if result := h.svc.Boot(); result.Relaunch {
		t.Fatal("restored binary must not relaunch again")
	}
	var reported []Outcome
	h.svc.Confirm(func(o Outcome) { reported = append(reported, o) })
	if len(reported) != 1 || reported[0].Result != "rolled_back" || reported[0].To != "1.1.0" || reported[0].From != "1.0.0" || reported[0].ActorUsername != "admin" {
		t.Fatalf("rollback report: %+v", reported)
	}
	if _, present, _ := readMarker(h.data); present {
		t.Fatal("marker must be cleared after the rollback is reported")
	}
	if status := h.svc.Status(context.Background()); status.LastOutcome == nil || status.LastOutcome.Result != "rolled_back" {
		t.Fatalf("status should surface the rollback: %+v", status.LastOutcome)
	}
}

func TestHealthyBootConfirmsUpdateOnce(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	installedAsUpdated(t, h)
	h.build("1.1.0")
	if result := h.svc.Boot(); result.Relaunch {
		t.Fatal("first boot must not roll back")
	}
	done := make(chan Outcome, 2)
	h.svc.Confirm(func(o Outcome) { done <- o })
	select {
	case outcome := <-done:
		if outcome.Result != "completed" || outcome.From != "1.0.0" || outcome.To != "1.1.0" || outcome.ActorUsername != "admin" {
			t.Fatalf("%+v", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("confirmation never fired")
	}
	if _, present, _ := readMarker(h.data); present {
		t.Fatal("marker must be removed once confirmed")
	}
	select {
	case <-done:
		t.Fatal("outcome reported twice")
	case <-time.After(100 * time.Millisecond):
	}
	// A later restart is an ordinary boot again.
	h.build("1.1.0")
	if result := h.svc.Boot(); result.Relaunch {
		t.Fatal("confirmed update must never roll back")
	}
}

func TestBootDiscardsStaleAndCorruptMarkers(t *testing.T) {
	// The recorded target is not what is running (swap undone by hand).
	h := newHarness(t, "v1.0.0", "1.1.0")
	if err := writeMarker(h.data, marker{State: markerApplied, From: "v1.0.0", To: "1.1.0", BootAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	if result := h.svc.Boot(); result.Relaunch {
		t.Fatal("a marker for a version that is not running must not trigger a rollback")
	}
	if _, present, _ := readMarker(h.data); present {
		t.Fatal("stale marker should be discarded")
	}
	// A corrupt marker never blocks startup.
	if err := os.MkdirAll(updatesDir(h.data), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath(h.data), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := h.svc.Boot(); result.Relaunch {
		t.Fatal("corrupt marker must be ignored")
	}
	if _, present, _ := readMarker(h.data); present {
		t.Fatal("corrupt marker should be discarded")
	}
}

func TestBootRemovesLeftoverStagedFile(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	if err := os.WriteFile(StagedPath(h.exe), fakeELF("orphan"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.svc.Boot()
	if _, err := os.Stat(StagedPath(h.exe)); !os.IsNotExist(err) {
		t.Fatal("an unverified leftover download must not survive a restart")
	}
}

func TestRollbackFailureDoesNotLoop(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	installedAsUpdated(t, h)
	if err := os.Remove(BackupPath(h.exe)); err != nil {
		t.Fatal(err)
	}
	var last BootResult
	for i := 0; i <= maxUnconfirmedBoots; i++ {
		h.build("1.1.0")
		last = h.svc.Boot()
	}
	if last.Relaunch {
		t.Fatal("without a backup there is nothing to relaunch into")
	}
	if _, present, _ := readMarker(h.data); present {
		t.Fatal("an unrecoverable marker must be dropped so it cannot loop")
	}
}

func TestReplaceExecutableRestoresOriginalWhenInstallFails(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "gamenode")
	if err := os.WriteFile(exe, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The staged file does not exist, so the second rename fails.
	if _, err := replaceExecutable(exe, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected failure")
	}
	if got, _ := os.ReadFile(exe); string(got) != "original" {
		t.Fatalf("original executable must be restored, got %q", got)
	}
}

func TestExecutableValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := validateExecutable(write("ok", fakeELF("x")), "linux"); err != nil {
		t.Errorf("valid ELF rejected: %v", err)
	}
	if validateExecutable(write("ok2", fakeELF("x")), "windows") == nil {
		t.Error("an ELF must not pass as a Windows executable")
	}
	arm := fakeELF("x")
	arm[18] = 0xB7 // EM_AARCH64
	if validateExecutable(write("arm", arm), "linux") == nil {
		t.Error("wrong CPU architecture must be rejected")
	}
	if validateExecutable(write("small", []byte("\x7fELF")), "linux") == nil {
		t.Error("tiny files must be rejected")
	}
	pe := make([]byte, minBinaryBytes+64)
	pe[0], pe[1] = 'M', 'Z'
	pe[0x3C] = 0x80
	copy(pe[0x80:], []byte{'P', 'E', 0, 0, 0x64, 0x86})
	if err := validateExecutable(write("pe.exe", pe), "windows"); err != nil {
		t.Errorf("valid PE rejected: %v", err)
	}
	pe[0x84], pe[0x85] = 0x4C, 0x01 // i386
	if validateExecutable(write("pe32.exe", pe), "windows") == nil {
		t.Error("32-bit PE must be rejected")
	}
	if validateExecutable(write("ok3", fakeELF("x")), "darwin") == nil {
		t.Error("unsupported OS must be rejected")
	}
}

func TestStatusNeverContainsHostPaths(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	h.stage()
	h.activity.RunningServers = 1
	h.svc.mu.Lock()
	h.svc.invalidateCacheLocked()
	h.svc.mu.Unlock()
	status := h.svc.Status(context.Background())
	encoded := mustJSON(t, status)
	for _, secret := range []string{h.dir, filepath.ToSlash(h.dir), h.exe, h.data} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("status leaked a host path %q: %s", secret, encoded)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSummaryIsCheapAndOmitsSafetyChecks(t *testing.T) {
	h := newHarness(t, "v1.0.0", "1.1.0")
	calls := 0
	h.svc.Bind(func(context.Context) (Activity, error) { calls++; return Activity{}, nil }, func(context.Context) error { calls++; return nil }, nil)
	_, _ = h.svc.Check(context.Background())
	calls = 0
	summary := h.svc.Summary()
	if !summary.UpdateAvailable || summary.Available == nil || len(summary.Checks) != 0 {
		t.Fatalf("summary: %+v", summary)
	}
	if calls != 0 {
		t.Fatalf("summary must not touch the server list or database, made %d calls", calls)
	}
	h.svc.mu.Lock()
	h.svc.invalidateCacheLocked() // the earlier Check populated the short-lived cache
	h.svc.mu.Unlock()
	if full := h.svc.Status(context.Background()); len(full.Checks) == 0 || calls == 0 {
		t.Fatal("the full status does evaluate the safety checks")
	}
}

// The updater downloads fixed asset names. This guards the contract with the
// release pipeline: if a workflow edit renames or drops an artifact, updates
// would silently stop working for every installed node.
func TestReleaseWorkflowPublishesTheAssetsTheUpdaterDownloads(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Skipf("release workflow not available: %v", err)
	}
	text := string(workflow)
	for _, platform := range [][2]string{{"windows", "amd64"}, {"linux", "amd64"}} {
		asset, ok := AssetName(platform[0], platform[1])
		if !ok {
			t.Fatalf("no asset for %v", platform)
		}
		if !strings.Contains(text, asset) {
			t.Errorf("release workflow no longer publishes %s", asset)
		}
	}
	if !strings.Contains(text, ChecksumAsset) {
		t.Errorf("release workflow no longer publishes %s", ChecksumAsset)
	}
	if !strings.Contains(text, "gamenode/internal/diagnostics.Version=") {
		t.Error("release binaries must embed their version; the updater refuses to run without it")
	}
}
