//go:build windows

package runtime

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeRuntimeStartStatusKill(t *testing.T) {
	ping, err := exec.LookPath("ping.exe")
	if err != nil {
		t.Skip("ping.exe unavailable")
	}
	r := NewNative()
	identity, _, err := r.Start(context.Background(), StartOptions{Executable: ping, Arguments: []string{"-t", "127.0.0.1"}, WorkingDirectory: filepath.Dir(ping)})
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
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("process remained running after kill")
}

func TestNativeRuntimeStop(t *testing.T) {
	ping, err := exec.LookPath("ping.exe")
	if err != nil {
		t.Skip("ping.exe unavailable")
	}
	r := NewNative()
	identity, _, err := r.Start(context.Background(), StartOptions{Executable: ping, Arguments: []string{"-t", "127.0.0.1"}, WorkingDirectory: filepath.Dir(ping)})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background(), identity, 2*time.Second); err != nil {
		t.Fatal(err)
	}
}
