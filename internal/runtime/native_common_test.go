package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStopTimeoutEscalates(t *testing.T) {
	called := false
	err := waitForExit(context.Background(), Identity{PID: 1, StartKey: "test"}, 20*time.Millisecond,
		func(context.Context, Identity) (Status, error) { return Status{Running: true, Known: true}, nil },
		func() error { called = true; return nil },
	)
	if err != nil || !called {
		t.Fatalf("force=%v err=%v", called, err)
	}
}

func TestEnvironmentOverrideIsNotDuplicated(t *testing.T) {
	entries := environment(map[string]string{"PATH": "controlled-path"})
	count := 0
	for _, entry := range entries {
		if strings.EqualFold(strings.SplitN(entry, "=", 2)[0], "PATH") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("PATH appeared %d times", count)
	}
}
