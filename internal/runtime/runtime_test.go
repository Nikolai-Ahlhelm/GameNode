//go:build !windows

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GAMENODE_RUNTIME_HELPER") == "1" {
		select {}
	}
}
func TestNativeRuntimeStartStatusKill(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := NewNative()
	identity, _, err := r.Start(context.Background(), StartOptions{Executable: exe, Arguments: []string{"-test.run=TestHelperProcess", "--"}, WorkingDirectory: filepath.Dir(exe), Environment: map[string]string{"GAMENODE_RUNTIME_HELPER": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := r.Status(context.Background(), identity)
	if err != nil || !status.Running || !status.Known {
		t.Fatalf("unexpected running status: %#v %v", status, err)
	}
	if err := r.Kill(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, _ = r.Status(context.Background(), identity)
		if !status.Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status.Running {
		t.Fatal("process remained running after kill")
	}
}
