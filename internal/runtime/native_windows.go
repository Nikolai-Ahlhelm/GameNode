//go:build windows

package runtime

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
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
