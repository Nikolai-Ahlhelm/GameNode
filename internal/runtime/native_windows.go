//go:build windows

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

type nativeRuntime struct{}

const stillActive = 259

func NewNative() Runtime { return nativeRuntime{} }

func (nativeRuntime) Start(_ context.Context, options StartOptions) (Identity, <-chan ExitResult, error) {
	cmd := exec.Command(options.Executable, options.Arguments...)
	cmd.Dir = options.WorkingDirectory
	cmd.Env = environment(options.Environment)
	if options.ConsoleInterruptCapable {
		// A console_interrupt server has all three standard handles piped for
		// GameNode's existing console capture, exactly like every other
		// managed server. With every standard handle explicitly redirected,
		// Windows does not auto-allocate a console for an otherwise
		// console-less child, so CREATE_NEW_PROCESS_GROUP alone can leave it
		// with no console at all to later target (verified empirically: a
		// piped-stdio child of a console-less GameNode gets no console
		// without CREATE_NEW_CONSOLE). CREATE_NEW_CONSOLE deterministically
		// gives it its own, exclusive console regardless of whether GameNode
		// itself has one (interactive terminal, no console, or Windows
		// service/Session 0). CREATE_NEW_CONSOLE alone does not reliably make
		// the new console's process group ID equal the process's own PID
		// (also verified empirically), so CREATE_NEW_PROCESS_GROUP is
		// combined with it; despite an easily misread MSDN remark that it is
		// "ignored" together with CREATE_NEW_CONSOLE, the combination is what
		// actually produces a process group ID equal to the new process's own
		// PID, so a later targeted CTRL_BREAK_EVENT reaches exactly this
		// process tree and never GameNode or a sibling server.
		// HideWindow keeps that console invisible; GameNode's own capture of
		// stdout/stderr/stdin is unaffected because it already uses pipes,
		// not the console screen buffer. Ordinary terminate/stdin_command
		// servers never set these flags, so their process creation is
		// unchanged.
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Identity{}, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Identity{}, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Identity{}, nil, err
	}
	if err := cmd.Start(); err != nil {
		return Identity{}, nil, err
	}
	if options.IO.Stdin != nil {
		options.IO.Stdin(stdin)
	}
	var copies sync.WaitGroup
	if options.IO.Stdout != nil {
		copies.Add(1)
		go func() { defer copies.Done(); _, _ = io.Copy(options.IO.Stdout, stdout) }()
	}
	if options.IO.Stderr != nil {
		copies.Add(1)
		go func() { defer copies.Done(); _, _ = io.Copy(options.IO.Stderr, stderr) }()
	}
	identity, err := windowsIdentity(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return Identity{}, nil, err
	}
	exits := make(chan ExitResult, 1)
	go func() {
		err := cmd.Wait()
		copies.Wait()
		exits <- ExitResult{ExitCode: exitCode(err), Err: err}
		close(exits)
	}()
	return identity, exits, nil
}

func (nativeRuntime) Stop(ctx context.Context, identity Identity, timeout time.Duration) error {
	if err := verifyWindows(identity); err != nil {
		return err
	}
	// taskkill may reject a non-forced termination. The documented Stop
	// contract is to wait for the configured timeout and then escalate.
	_ = taskkill(ctx, identity.PID, false)
	return waitForExit(ctx, identity, timeout, nativeRuntime{}.Status, func() error {
		return nativeRuntime{}.Kill(context.Background(), identity)
	})
}

// consoleSignalEnv names the environment variable that switches this exact
// compiled GameNode binary into a disposable, single-purpose console-signal
// helper process. It is set only by Interrupt on the child it launches; no
// other GameNode code path reads or writes it. See RunConsoleSignalHelper.
const consoleSignalEnv = "GAMENODE_CONSOLE_SIGNAL_TARGET_PID"

// Helper process exit codes. 0 means the event was delivered; every other
// value is a stable, non-sensitive outcome classification and never encodes
// an OS error, handle, or PID.
const (
	consoleSignalExitDelivered   = 0
	consoleSignalExitUnsupported = 10
	consoleSignalExitFailed      = 11
	consoleSignalExitBadRequest  = 12
)

// newConsoleSignalHelperCmd builds the command used to invoke this exact
// compiled binary as the disposable console-signal helper described on
// Interrupt. It is a variable only so the Windows test suite can point it at
// this test binary's own single dedicated entry point instead of relaunching
// every test in the package recursively; production code never overrides it,
// and every non-test build only ever gets this one implementation.
var newConsoleSignalHelperCmd = func(ctx context.Context, pid int) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = []string{consoleSignalEnv + "=" + strconv.Itoa(pid)}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd, nil
}

// Interrupt delivers a targeted CTRL_BREAK_EVENT to identity.PID's own
// console process group. GenerateConsoleCtrlEvent only succeeds for a caller
// sharing the target's console, which GameNode itself may not have (started
// as a Windows service, or with no visible console at all). Rather than
// mutate GameNode's own, potentially concurrently-used console attachment,
// Interrupt re-execs this same compiled binary as a short-lived, disposable
// helper (see RunConsoleSignalHelper) whose entire job is to attach to the
// target's console, generate the event, and exit. The helper never inherits
// a console of its own (CREATE_NO_WINDOW) and is never a member of the
// target's process group, so it cannot receive the event it sends and cannot
// be confused with a shell, script, or persistent second service.
func (nativeRuntime) Interrupt(ctx context.Context, identity Identity) error {
	if err := verifyWindows(identity); err != nil {
		return err
	}
	cmd, err := newConsoleSignalHelperCmd(ctx, identity.PID)
	if err != nil {
		return ErrConsoleInterruptUnsupported
	}
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		// The helper never started or its exit status could not be read; this
		// is not an OS-level control-event failure but an environment problem.
		return ErrConsoleInterruptUnsupported
	}
	switch cmd.ProcessState.ExitCode() {
	case consoleSignalExitDelivered:
		return nil
	case consoleSignalExitUnsupported:
		return ErrConsoleInterruptUnsupported
	default:
		return ErrConsoleInterruptFailed
	}
}

// RunConsoleSignalHelper must be the first call in main(). If this process
// invocation is the disposable console-signal helper described on Interrupt
// (identified solely by consoleSignalEnv, never by an argv flag), it
// attaches to the requested process's console, generates a scoped
// CTRL_BREAK_EVENT for that process's own group, detaches again, and returns
// a stable exit code with ok=true. main must os.Exit(code) immediately
// without performing any normal GameNode startup. ok is false for every
// normal GameNode invocation.
func RunConsoleSignalHelper() (int, bool) {
	raw, present := os.LookupEnv(consoleSignalEnv)
	if !present {
		return 0, false
	}
	pid, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || pid == 0 {
		return consoleSignalExitBadRequest, true
	}
	// A process must free any console of its own before attaching to a
	// different one. This helper is spawned fresh for exactly this purpose
	// and never had a console to preserve.
	freeConsole()
	if err := attachConsole(uint32(pid)); err != nil {
		return consoleSignalExitUnsupported, true
	}
	defer freeConsole()
	// Documented Microsoft guidance: a process that calls
	// GenerateConsoleCtrlEvent should first disable its own default control
	// handler so it cannot be torn down by the very event it is about to
	// broadcast into the shared console. The event itself is still scoped to
	// the target's process group and this helper is not a member of it, so
	// this is defense in depth, not the primary isolation mechanism.
	ignoreOwnConsoleCtrl()
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid)); err != nil {
		return consoleSignalExitFailed, true
	}
	return consoleSignalExitDelivered, true
}

// AttachConsole, FreeConsole, and SetConsoleCtrlHandler are not exposed by
// golang.org/x/sys/windows; they are loaded directly from kernel32.dll.
var (
	modKernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole         = modKernel32.NewProc("AttachConsole")
	procFreeConsole           = modKernel32.NewProc("FreeConsole")
	procSetConsoleCtrlHandler = modKernel32.NewProc("SetConsoleCtrlHandler")
)

func attachConsole(pid uint32) error {
	ok, _, callErr := procAttachConsole.Call(uintptr(pid))
	if ok == 0 {
		return callErr
	}
	return nil
}

func freeConsole() {
	_, _, _ = procFreeConsole.Call()
}

func ignoreOwnConsoleCtrl() {
	_, _, _ = procSetConsoleCtrlHandler.Call(0, 1)
}

func (nativeRuntime) Kill(ctx context.Context, identity Identity) error {
	if err := verifyWindows(identity); err != nil {
		return err
	}
	if err := taskkill(ctx, identity.PID, true); err == nil {
		return nil
	}
	return terminateWindowsRoot(identity.PID)
}

func (nativeRuntime) Status(_ context.Context, identity Identity) (Status, error) {
	if identity.PID <= 0 || identity.StartKey == "" {
		return Status{Known: false}, nil
	}
	err := verifyWindows(identity)
	if err == nil {
		return Status{Running: true, Known: true}, nil
	}
	if err == ErrIdentityMismatch {
		return Status{Known: false}, nil
	}
	if err == ErrNotRunning {
		return Status{Running: false, Known: true}, nil
	}
	return Status{}, err
}

func taskkill(ctx context.Context, pid int, force bool) error {
	args := []string{"/PID", strconv.Itoa(pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	output, err := exec.CommandContext(ctx, "taskkill.exe", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("taskkill: %w: %s", err, string(output))
	}
	return nil
}

func windowsIdentity(pid int) (Identity, error) {
	key, err := windowsStartKey(pid)
	if err != nil {
		return Identity{}, err
	}
	return Identity{PID: pid, StartKey: key}, nil
}

func verifyWindows(identity Identity) error {
	key, err := windowsStartKey(identity.PID)
	if err == windows.ERROR_INVALID_PARAMETER {
		return ErrNotRunning
	}
	if err != nil {
		return err
	}
	if key != identity.StartKey {
		return ErrIdentityMismatch
	}
	return nil
}

func windowsStartKey(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return "", err
	}
	if exitCode != stillActive {
		return "", ErrNotRunning
	}
	return fmt.Sprintf("%d", created.Nanoseconds()), nil
}

func terminateWindowsRoot(pid int) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.TerminateProcess(handle, 1)
}
