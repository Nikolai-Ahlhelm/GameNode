//go:build windows

package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// consoleInterruptHelperEnv selects the behavior of
// TestConsoleInterruptHelperProcess when it is re-exec'd as a Go test binary
// child by the tests below. "report" registers a real Windows console control
// handler, prints "ready", reports the first control event it actually
// receives on stdout, and exits 0. "ignore" registers a handler that
// swallows every event and never exits on its own, so callers can exercise
// the Stop timeout/force-kill fallback deterministically without depending
// on any external helper binary or script.
const consoleInterruptHelperEnv = "GAMENODE_CONSOLE_INTERRUPT_HELPER_MODE"

// init points Interrupt's helper-process launcher at TestConsoleSignalHelperEntry
// (below), selected with "-test.run", instead of the real, unfiltered
// os.Executable() used in production.
//
// Under `go test`, os.Executable() resolves to this package's compiled test
// binary, not cmd/gamenode's production binary. That test binary's own
// main() is testing.Main, which — given no "-test.run" filter — runs every
// test in the package, including these Interrupt tests themselves. Calling
// the production newConsoleSignalHelperCmd unmodified from inside a test
// would therefore re-launch the entire suite recursively on every single
// Interrupt() call instead of the intended one-shot disposable helper. This
// override is test-only; production code always uses the unmodified
// implementation defined in native_windows.go.
func init() {
	newConsoleSignalHelperCmd = func(ctx context.Context, pid int) (*exec.Cmd, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, exe, "-test.run=TestConsoleSignalHelperEntry", "--")
		cmd.Env = []string{consoleSignalEnv + "=" + strconv.Itoa(pid)}
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		return cmd, nil
	}
}

// TestConsoleSignalHelperEntry is not a real test; under the init() override
// above it is the single test selected in the re-exec'd helper process. It
// mirrors exactly what cmd/gamenode's real main() does in production: check
// the env var first, and if present, run the helper and exit immediately
// without executing anything else (here, without running other tests).
func TestConsoleSignalHelperEntry(t *testing.T) {
	if _, present := os.LookupEnv(consoleSignalEnv); !present {
		return
	}
	code, _ := RunConsoleSignalHelper()
	os.Exit(code)
}

var consoleInterruptReceived = make(chan uint32, 1)

// consoleInterruptHandlerRoutine is the PHANDLER_ROUTINE callback registered
// by the helper process below. It only records the event; it never performs
// process-wide state changes and never calls GenerateConsoleCtrlEvent itself.
func consoleInterruptHandlerRoutine(ctrlType uint32) uintptr {
	select {
	case consoleInterruptReceived <- ctrlType:
	default:
	}
	return 1 // TRUE: handled, stop further handler processing.
}

// TestConsoleInterruptHelperProcess is not a real test; it is the disposable
// Go-test-binary helper described in CONSOLE-INTERRUPT.md section 13. It only
// acts when consoleInterruptHelperEnv is set, matching the existing
// self-exec pattern in internal/servers/console_windows_test.go.
func TestConsoleInterruptHelperProcess(t *testing.T) {
	mode := os.Getenv(consoleInterruptHelperEnv)
	if mode == "" {
		return
	}
	callback := syscall.NewCallback(func(ctrlType uint32) uintptr { return consoleInterruptHandlerRoutine(ctrlType) })
	ok, _, callErr := procSetConsoleCtrlHandler.Call(callback, 1)
	if ok == 0 {
		fmt.Fprintln(os.Stdout, "handler-registration-failed:", callErr)
		os.Exit(3)
	}
	fmt.Fprintln(os.Stdout, "ready")
	// A registered console control handler is invoked by Windows on its own
	// OS thread outside Go's scheduler. The Go runtime's deadlock detector
	// does not know about that pending native callback: a bare `select {}`
	// or an unguarded `<-consoleInterruptReceived` with no other runnable
	// goroutine and no pending timer is indistinguishable, to the Go
	// runtime, from a genuine deadlock, and it kills the process with
	// "fatal error: all goroutines are asleep - deadlock!" before the
	// control event can ever arrive. A bounded select with time.After keeps
	// a timer alive so the process legitimately waits instead.
	deadline := time.After(20 * time.Second)
	if mode == "ignore" {
		// The parent test is exercising the timeout/force-kill fallback and
		// owns ending this process via Kill; just outlast the parent's own
		// bounded wait instead of blocking forever.
		<-deadline
		os.Exit(4)
	}
	select {
	case received := <-consoleInterruptReceived:
		fmt.Fprintf(os.Stdout, "received:%d\n", received)
		os.Exit(0)
	case <-deadline:
		fmt.Fprintln(os.Stdout, "no-event-received")
		os.Exit(5)
	}
}

type helperOutput struct {
	lines chan string
}

func startInterruptHelper(t *testing.T, r Runtime, mode string) (Identity, <-chan ExitResult, *helperOutput) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output := &helperOutput{lines: make(chan string, 16)}
	identity, exits, err := r.Start(context.Background(), StartOptions{
		Executable:              exe,
		Arguments:               []string{"-test.run=TestConsoleInterruptHelperProcess", "--"},
		WorkingDirectory:        filepath.Dir(exe),
		Environment:             map[string]string{consoleInterruptHelperEnv: mode},
		ConsoleInterruptCapable: true,
		IO:                      StartIO{Stdout: writer},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	buf := make([]byte, 256)
	n, readErr := reader.Read(buf)
	if readErr != nil || n == 0 {
		t.Fatalf("did not receive helper ready line: n=%d err=%v", n, readErr)
	}
	line := strings.TrimSpace(string(buf[:n]))
	if line != "ready" {
		t.Fatalf("expected ready, got %q", line)
	}
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			output.lines <- scanner.Text()
		}
		close(output.lines)
	}()
	return identity, exits, output
}

func waitHelperLine(t *testing.T, output *helperOutput, prefix string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-output.lines:
			if !ok {
				t.Fatalf("helper output closed before %q", prefix)
			}
			if strings.HasPrefix(line, prefix) {
				return line
			}
		case <-deadline:
			t.Fatalf("timed out waiting for helper output %q", prefix)
		}
	}
}

// TestNativeRuntimeInterruptDeliversTargetedEvent covers CONSOLE-INTERRUPT.md
// section 12 "Interrupt": a helper started with ConsoleInterruptCapable
// receives exactly CTRL_BREAK_EVENT (1) and exits itself, with no force-kill.
func TestNativeRuntimeInterruptDeliversTargetedEvent(t *testing.T) {
	r := NewNative()
	identity, exits, output := startInterruptHelper(t, r, "report")
	if err := r.Interrupt(context.Background(), identity); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	line := waitHelperLine(t, output, "received:")
	if line != "received:1" {
		t.Fatalf("expected CTRL_BREAK_EVENT (1), got %q", line)
	}
	select {
	case exit := <-exits:
		if exit.ExitCode != 0 {
			t.Fatalf("unexpected exit code %d (err=%v)", exit.ExitCode, exit.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not exit after receiving the interrupt")
	}
}

// TestNativeRuntimeInterruptIsolatesProcessGroups covers section 12
// "Isolation": interrupting server A must not affect server B.
func TestNativeRuntimeInterruptIsolatesProcessGroups(t *testing.T) {
	r := NewNative()
	identityA, exitsA, outputA := startInterruptHelper(t, r, "report")
	identityB, exitsB, _ := startInterruptHelper(t, r, "ignore")
	defer func() { _ = r.Kill(context.Background(), identityB) }()

	if err := r.Interrupt(context.Background(), identityA); err != nil {
		t.Fatalf("interrupt A: %v", err)
	}
	waitHelperLine(t, outputA, "received:")
	select {
	case exit := <-exitsA:
		if exit.ExitCode != 0 {
			t.Fatalf("unexpected exit code for A: %d", exit.ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper A did not exit")
	}

	status, err := r.Status(context.Background(), identityB)
	if err != nil || !status.Running {
		t.Fatalf("interrupting A affected sibling B: status=%#v err=%v", status, err)
	}
	select {
	case <-exitsB:
		t.Fatal("helper B exited even though only A was interrupted")
	default:
	}
}

// TestNativeRuntimeInterruptIgnoredLeavesProcessRunning covers the runtime
// half of section 12 "Timeout": a process that ignores the control event
// keeps running, so the caller-owned timeout/force-kill path (implemented in
// internal/servers) has a real signal delivered without the process exiting
// to fall back from. internal/servers/console_interrupt_windows_test.go
// exercises the full bounded fallback through the normal Service.Stop path.
func TestNativeRuntimeInterruptIgnoredLeavesProcessRunning(t *testing.T) {
	r := NewNative()
	identity, exits, _ := startInterruptHelper(t, r, "ignore")
	if err := r.Interrupt(context.Background(), identity); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	status, err := r.Status(context.Background(), identity)
	if err != nil || !status.Running {
		t.Fatalf("helper unexpectedly stopped after ignoring the interrupt: status=%#v err=%v", status, err)
	}
	if err = r.Kill(context.Background(), identity); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-exits:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not exit after kill")
	}
}

// TestNativeRuntimeInterruptRejectsWrongIdentity covers section 12
// "Isolation": a mismatched PID or StartKey must never receive a signal.
func TestNativeRuntimeInterruptRejectsWrongIdentity(t *testing.T) {
	r := NewNative()
	identity, exits, _ := startInterruptHelper(t, r, "ignore")
	defer func() { _ = r.Kill(context.Background(), identity) }()

	stale := Identity{PID: identity.PID, StartKey: identity.StartKey + "-stale"}
	if err := r.Interrupt(context.Background(), stale); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch for a stale StartKey, got %v", err)
	}
	select {
	case <-exits:
		t.Fatal("helper exited despite a rejected stale identity")
	default:
	}
}

// TestNativeRuntimeInterruptAlreadyExited covers section 12 "Context":
// interrupting an identity that is already gone must fail safely, never
// silently "succeed".
func TestNativeRuntimeInterruptAlreadyExited(t *testing.T) {
	r := NewNative()
	identity, exits, _ := startInterruptHelper(t, r, "report")
	if err := r.Kill(context.Background(), identity); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-exits:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not exit after kill")
	}
	if err := r.Interrupt(context.Background(), identity); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("expected ErrNotRunning for an already-exited process, got %v", err)
	}
}

// TestNativeRuntimeInterruptCancelledContext covers section 12 "Context":
// a context cancelled before delivery must fail safely rather than hang or
// silently claim success.
func TestNativeRuntimeInterruptCancelledContext(t *testing.T) {
	r := NewNative()
	identity, exits, _ := startInterruptHelper(t, r, "ignore")
	defer func() { _ = r.Kill(context.Background(), identity) }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Interrupt(ctx, identity)
	if err == nil {
		t.Fatal("expected an error for an already-cancelled context")
	}
	select {
	case <-exits:
		t.Fatal("helper exited despite a cancelled-context interrupt attempt")
	default:
	}
}

// TestConsoleSignalHelperDispatch covers RunConsoleSignalHelper's own
// classification of a request, independent of any spawned subprocess.
func TestConsoleSignalHelperDispatch(t *testing.T) {
	if code, ok := RunConsoleSignalHelper(); ok || code != 0 {
		t.Fatalf("expected no dispatch without the env var, got code=%d ok=%v", code, ok)
	}
	t.Setenv(consoleSignalEnv, "not-a-number")
	if code, ok := RunConsoleSignalHelper(); !ok || code != consoleSignalExitBadRequest {
		t.Fatalf("expected a bad-request exit code, got code=%d ok=%v", code, ok)
	}
	// PID 1 is never a real, attachable console owner on Windows (well-known
	// low PIDs belong to the System Idle Process/System and have no
	// user-attachable console), so this deterministically exercises the
	// "delivery attempted but unsupported" outcome without depending on any
	// other process happening to be present.
	t.Setenv(consoleSignalEnv, "1")
	if code, ok := RunConsoleSignalHelper(); !ok || code != consoleSignalExitUnsupported {
		t.Fatalf("expected an unsupported exit code for an unattachable target, got code=%d ok=%v", code, ok)
	}
}

// TestNativeRuntimeStartConsoleInterruptCapableDoesNotAffectOrdinaryStart
// covers section 12 "Windows Start": a terminate-style start (no
// ConsoleInterruptCapable) is unaffected by the new flag's existence, and
// stdin/stdout/stderr and process identity remain intact either way.
func TestNativeRuntimeStartConsoleInterruptCapableDoesNotAffectOrdinaryStart(t *testing.T) {
	ping, err := exec.LookPath("ping.exe")
	if err != nil {
		t.Skip("ping.exe unavailable")
	}
	r := NewNative()
	identity, exits, err := r.Start(context.Background(), StartOptions{Executable: ping, Arguments: []string{"-n", "1", "127.0.0.1"}, WorkingDirectory: filepath.Dir(ping)})
	if err != nil {
		t.Fatal(err)
	}
	if identity.PID == 0 || identity.StartKey == "" {
		t.Fatalf("ordinary start did not receive a valid identity: %#v", identity)
	}
	select {
	case <-exits:
	case <-time.After(10 * time.Second):
		t.Fatal("ordinary ping helper did not exit")
	}
}
