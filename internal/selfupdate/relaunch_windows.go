package selfupdate

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// relaunch starts exe as a detached successor. Windows cannot replace a
// process image in place. The caller must already have released the database
// and listeners so the successor can bind them; game processes are not tied
// to this process and keep running (see docs/runtime.md). The successor is
// detached from this console and writes to the normal GameNode log files.
func relaunch(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
