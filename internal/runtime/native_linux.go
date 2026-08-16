//go:build linux

package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type nativeRuntime struct{}

func NewNative() Runtime { return nativeRuntime{} }

func (nativeRuntime) Start(_ context.Context, options StartOptions) (Identity, <-chan ExitResult, error) {
	if options.ConsoleInterruptCapable {
		// console_interrupt is a compiled, Windows-only stop primitive (see
		// native_windows.go); there is no Linux console-control-event
		// concept to honor it with. Refuse the start defensively instead of
		// silently ignoring the flag and starting a server that could never
		// receive a real graceful interrupt through this mechanism — the
		// caller would otherwise only discover the gap much later, at stop
		// time. Official templates already reject console_interrupt for any
		// non-Windows platform launch at validation time; this is the
		// runtime-side backstop for that same invariant.
		return Identity{}, nil, ErrConsoleInterruptUnsupported
	}
	cmd := exec.Command(options.Executable, options.Arguments...)
	cmd.Dir = options.WorkingDirectory
	cmd.Env = environment(options.Environment)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	identity, err := linuxIdentity(cmd.Process.Pid)
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
	if err := verifyLinux(identity); err != nil {
		return err
	}
	if err := syscall.Kill(-identity.PID, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	return waitForExit(ctx, identity, timeout, nativeRuntime{}.Status, func() error {
		return syscall.Kill(-identity.PID, syscall.SIGKILL)
	})
}

// Interrupt has no Linux implementation. Linux servers use SIGTERM through
// Stop; there is no Windows console control event concept here. This is a
// stable interface stub, not an emulation, so it always reports the
// documented unsupported error rather than silently no-op succeeding.
func (nativeRuntime) Interrupt(_ context.Context, _ Identity) error {
	return ErrConsoleInterruptUnsupported
}

// RunConsoleSignalHelper has no Linux behavior; GameNode never re-execs
// itself as a console-signal helper on this platform. It always reports that
// this invocation is not the helper.
func RunConsoleSignalHelper() (int, bool) { return 0, false }

func (nativeRuntime) Kill(_ context.Context, identity Identity) error {
	if err := verifyLinux(identity); err != nil {
		return err
	}
	if err := syscall.Kill(-identity.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func (nativeRuntime) Status(_ context.Context, identity Identity) (Status, error) {
	if identity.PID <= 0 || identity.StartKey == "" {
		return Status{Known: false}, nil
	}
	err := verifyLinux(identity)
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

func linuxIdentity(pid int) (Identity, error) {
	key, err := linuxStartKey(pid)
	if err != nil {
		return Identity{}, err
	}
	return Identity{PID: pid, StartKey: key}, nil
}

func verifyLinux(identity Identity) error {
	key, err := linuxStartKey(identity.PID)
	if os.IsNotExist(err) {
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

func linuxStartKey(pid int) (string, error) {
	f, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(line, ")")
	if end < 0 {
		return "", fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("invalid proc stat fields")
	}
	return fields[19], nil
}
