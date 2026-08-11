package steamcmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type zipDownloader struct {
	calls   atomic.Int32
	corrupt bool
}

func (d *zipDownloader) Download(_ context.Context, destination string) error {
	d.calls.Add(1)
	if d.corrupt {
		return os.WriteFile(destination, []byte("corrupt"), 0600)
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("steamcmd.exe")
	if err == nil {
		_, err = entry.Write([]byte("binary"))
	}
	closeErr := writer.Close()
	file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

type captureRunner struct {
	command Command
	result  Result
	err     error
	wait    bool
}

func (r *captureRunner) Run(ctx context.Context, command Command) (Result, error) {
	r.command = command
	if r.wait {
		<-ctx.Done()
		return Result{ExitCode: -1}, ctx.Err()
	}
	return r.result, r.err
}

func TestBuildArgumentsAndValidation(t *testing.T) {
	plan := InstallPlan{AppID: 294420, Validate: true, BetaBranch: "latest_experimental", LoginMode: "anonymous"}
	args := BuildArguments(`C:\servers\seven`, plan)
	want := []string{"+force_install_dir", `C:\servers\seven`, "+login", "anonymous", "+app_update", "294420", "-beta", "latest_experimental", "validate", "+quit"}
	if len(args) != len(want) {
		t.Fatalf("args=%#v", args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d=%q", i, args[i])
		}
	}
	for _, branch := range []string{"bad branch", "+quit", "../beta", ";evil"} {
		plan.BetaBranch = branch
		if ValidatePlan(plan) == nil {
			t.Fatalf("accepted branch %q", branch)
		}
	}
	plan.BetaBranch = ""
	plan.LoginMode = "credentials_required"
	if ValidatePlan(plan) == nil {
		t.Fatal("credentialed login accepted")
	}
	plan.LoginMode = "anonymous"
	plan.AppID = 0
	if ValidatePlan(plan) == nil {
		t.Fatal("invalid app id accepted")
	}
}

func TestManagedBootstrapIsAtomicAndSingleflight(t *testing.T) {
	base := t.TempDir()
	downloader := &zipDownloader{}
	manager := New(filepath.Join(base, "tools", "steamcmd"), Platform{"windows", WindowsURL, "zip", "steamcmd.exe"}, downloader, &captureRunner{})
	var group sync.WaitGroup
	errorsFound := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); errorsFound <- manager.Ensure(context.Background(), nil) }()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if downloader.calls.Load() != 1 {
		t.Fatalf("downloads=%d", downloader.calls.Load())
	}
	if !manager.Detect() {
		t.Fatal("managed SteamCMD not detected")
	}
	if entries, _ := os.ReadDir(filepath.Join(base, "tools")); len(entries) != 1 {
		t.Fatalf("unexpected bootstrap artifacts: %d", len(entries))
	}
}

func TestBootstrapRejectsCorruptAndIncompleteArchives(t *testing.T) {
	for _, test := range []struct {
		name       string
		downloader Downloader
	}{{"corrupt", &zipDownloader{corrupt: true}}, {"missing executable", downloaderFunc(func(_ context.Context, destination string) error {
		file, _ := os.Create(destination)
		writer := zip.NewWriter(file)
		entry, _ := writer.Create("other.txt")
		entry.Write([]byte("x"))
		writer.Close()
		return file.Close()
	})}} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "steamcmd")
			manager := New(root, Platform{"windows", WindowsURL, "zip", "steamcmd.exe"}, test.downloader, nil)
			if manager.Ensure(context.Background(), nil) == nil {
				t.Fatal("invalid archive accepted")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("partial managed installation committed")
			}
		})
	}
}

type downloaderFunc func(context.Context, string) error

func (f downloaderFunc) Download(ctx context.Context, path string) error { return f(ctx, path) }

func TestInstallUsesStructuredCommandAndCancellation(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "tool")
	os.MkdirAll(tool, 0700)
	os.WriteFile(filepath.Join(tool, "steamcmd.exe"), []byte("x"), 0600)
	target := filepath.Join(root, "server")
	os.Mkdir(target, 0700)
	runner := &captureRunner{}
	manager := New(tool, Platform{"windows", WindowsURL, "zip", "steamcmd.exe"}, nil, runner)
	plan := InstallPlan{AppID: 294420, Validate: true, LoginMode: "anonymous"}
	if err := manager.Install(context.Background(), target, plan, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if runner.command.Executable != filepath.Join(tool, "steamcmd.exe") || runner.command.WorkingDirectory != tool {
		t.Fatalf("command=%#v", runner.command)
	}
	if runner.command.Arguments[1] != target {
		t.Fatal("target was not a distinct argument")
	}
	runner.wait = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Install(ctx, target, plan, io.Discard, nil) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestArchivePathSafety(t *testing.T) {
	names := []string{"../evil", "/absolute", "C:/drive", "\\\\server\\share", "safe/../../evil", "safe\\..\\evil"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.zip")
			file, _ := os.Create(archive)
			writer := zip.NewWriter(file)
			entry, _ := writer.Create(name)
			entry.Write([]byte("x"))
			writer.Close()
			file.Close()
			destination := t.TempDir()
			if Extract("zip", archive, destination) == nil {
				t.Fatalf("accepted %q", name)
			}
		})
	}
	archive := filepath.Join(t.TempDir(), "link.tar.gz")
	createTar(t, archive, &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside"}, nil)
	if Extract("tar.gz", archive, t.TempDir()) == nil {
		t.Fatal("tar symlink accepted")
	}
}

func TestArchiveExtractionValid(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "valid.tar.gz")
	createTar(t, archive, &tar.Header{Name: "linux32/steamcmd", Typeflag: tar.TypeReg, Mode: 0755, Size: 3}, []byte("bin"))
	destination := t.TempDir()
	if err := Extract("tar.gz", archive, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "linux32", "steamcmd"))
	if err != nil || string(data) != "bin" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func createTar(t *testing.T, path string, header *tar.Header, data []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	writer := tar.NewWriter(gzipWriter)
	if err = writer.WriteHeader(header); err == nil && len(data) > 0 {
		_, err = writer.Write(data)
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	gzipWriter.Close()
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
func TestHTTPDownloaderTrustAndLimits(t *testing.T) {
	if err := NewHTTPDownloader("https://example.invalid/steamcmd.zip").Download(context.Background(), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("arbitrary source accepted")
	}
	if err := NewHTTPDownloader("https://steamcdn-a.akamaihd.net/client/installer/other.zip").Download(context.Background(), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("non-allowlisted Valve path accepted")
	}
	downloader := NewHTTPDownloader(WindowsURL)
	downloader.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("archive"))), ContentLength: MaxArchiveBytes + 1, Header: http.Header{}}, nil
	})}
	if err := downloader.Download(context.Background(), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestNativeRunnerCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("process cancellation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NativeRunner{}.Run(ctx, Command{Executable: filepath.Join(t.TempDir(), "missing"), Output: io.Discard})
	if err == nil {
		t.Fatal("cancelled command succeeded")
	}
	_ = time.Second
}
