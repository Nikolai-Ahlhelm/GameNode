//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// relaunch replaces the current process image with exe, keeping the PID (and
// therefore any supervisor's view of the service) unchanged. Game processes
// started by GameNode remain children of this PID and keep running; they are
// rediscovered by the new image exactly as after any GameNode restart. The
// caller must already have released the database, listeners, and log files:
// nothing deferred runs after a successful exec.
func relaunch(exe string, args []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}
