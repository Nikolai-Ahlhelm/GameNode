package runtime

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrNotRunning = errors.New("process is not running")
var ErrIdentityMismatch = errors.New("process identity does not match")

type StartOptions struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	Environment      map[string]string
	IO               StartIO
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
	Kill(context.Context, Identity) error
	Status(context.Context, Identity) (Status, error)
	Metrics(context.Context, Identity) (Metrics, error)
}
