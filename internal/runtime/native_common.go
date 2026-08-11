package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func environment(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if hasEnvironmentKey(overrides, key) {
			continue
		}
		env = append(env, item)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func hasEnvironmentKey(overrides map[string]string, key string) bool {
	for candidate := range overrides {
		if candidate == key || (runtime.GOOS == "windows" && strings.EqualFold(candidate, key)) {
			return true
		}
	}
	return false
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func waitForExit(ctx context.Context, identity Identity, timeout time.Duration, status func(context.Context, Identity) (Status, error), force func() error) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		current, err := status(ctx, identity)
		if err != nil {
			return err
		}
		if !current.Running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return force()
		case <-tick.C:
		}
	}
}
