package runtime

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrNotRunning = errors.New("process is not running")
var ErrIdentityMismatch = errors.New("process identity does not match")

// ErrConsoleInterruptUnsupported means a Windows console control event cannot
// be safely and specifically addressed to this process identity. Callers must
// not claim a graceful shutdown attempt was made when this is returned; the
// stable API/audit code is CodeConsoleInterruptUnsupported.
var ErrConsoleInterruptUnsupported = errors.New("console interrupt is not supported for this process")

// ErrConsoleInterruptFailed means a targeted console control event was
// attempted but OS-level delivery failed. The stable API/audit code is
// CodeConsoleInterruptFailed.
var ErrConsoleInterruptFailed = errors.New("console interrupt could not be delivered")

// Stable, safe error codes surfaced to API responses and audit records for the
// console-interrupt stop path. They never carry OS error text, PIDs, or
// handles; see docs/security.md.
const (
	CodeConsoleInterruptUnsupported = "RUNTIME_CONSOLE_INTERRUPT_UNSUPPORTED"
	CodeConsoleInterruptFailed      = "RUNTIME_CONSOLE_INTERRUPT_FAILED"
)

type StartOptions struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	Environment      map[string]string
	IO               StartIO
	// ConsoleInterruptCapable requests that Windows start this process as the
	// root of its own console process group (CREATE_NEW_PROCESS_GROUP) so a
	// later Interrupt can target it precisely without reaching GameNode or any
	// sibling server. Callers derive this from the server's normalized stop
	// method; it is ignored on platforms without the concept. It must not be
	// set for ordinary terminate/stdin_command servers so their existing
	// process-creation behavior is unchanged.
	ConsoleInterruptCapable bool
}

// StartIO is deliberately transport-neutral. The runtime only owns OS pipes
// and copies output to these sinks; orchestration belongs to servers.Service.
type StartIO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  func(io.WriteCloser)
}

type Identity struct {
	PID      int    `json:"pid"`
	StartKey string `json:"-"`
}

type ExitResult struct {
	ExitCode int
	Err      error
}

type Status struct {
	Running bool
	Known   bool
}

// Metrics contains identity-verified, cumulative process values. CPUTime is
// converted into a percentage by the monitoring service between samples.
type Metrics struct {
	CPUTime     time.Duration
	MemoryBytes uint64
	ThreadCount uint32
	HandleCount uint32
}

type Runtime interface {
	Start(context.Context, StartOptions) (Identity, <-chan ExitResult, error)
	Stop(context.Context, Identity, time.Duration) error
	// Interrupt delivers one targeted, non-broadcast graceful-shutdown signal
	// to a process previously started with ConsoleInterruptCapable and
	// verifies its identity first. It only sends the signal; the caller is
	// responsible for waiting on the normal exit path and falling back to
	// Kill after its own bounded timeout. It returns
	// ErrConsoleInterruptUnsupported when this process identity cannot be
	// addressed safely (including on platforms without the primitive) and
	// ErrConsoleInterruptFailed when delivery was attempted but failed.
	Interrupt(context.Context, Identity) error
	Kill(context.Context, Identity) error
	Status(context.Context, Identity) (Status, error)
	Metrics(context.Context, Identity) (Metrics, error)
}
