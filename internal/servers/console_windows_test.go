//go:build windows

package servers

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/console"
	"gamenode/internal/database"
	"gamenode/internal/runtime"
)

func TestWindowsConsoleIOSmoke(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	manager := console.NewManager()
	service := NewService(NewStore(db), runtime.NewNative(), manager)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Create(context.Background(), Server{
		Name:                 "console smoke",
		CreationMode:         CreationCustom,
		WorkingDirectory:     filepath.Dir(executable),
		Executable:           executable,
		Arguments:            []string{"-test.run=TestWindowsConsoleIOHelper", "--"},
		EnvironmentVariables: map[string]string{"GAMENODE_CONSOLE_SMOKE": "1"},
		StopTimeoutSeconds:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	old, ok := manager.CurrentSession(record.Server.ID)
	if !ok {
		t.Fatal("initial console session missing")
	}
	waitConsoleOutput(t, old, "stdout", "stdout-ready")
	waitConsoleOutput(t, old, "stderr", "stderr-ready")
	if err = old.Input("hello\n"); err != nil {
		t.Fatal(err)
	}
	waitConsoleOutput(t, old, "stdout", "echo:hello")

	if _, err = service.Restart(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	current, ok := manager.CurrentSession(record.Server.ID)
	if !ok || current.ID == old.ID || current.InstanceID == old.InstanceID {
		t.Fatal("restart did not replace console session and instance")
	}
	if err = old.Input("stale\n"); err == nil {
		t.Fatal("old stdin remained usable after restart")
	}
	waitConsoleOutput(t, current, "stdout", "stdout-ready")
	waitConsoleOutput(t, current, "stderr", "stderr-ready")
	if err = current.Input("again\n"); err != nil {
		t.Fatal(err)
	}
	waitConsoleOutput(t, current, "stdout", "echo:again")
	if err = current.Input("quit\n"); err != nil {
		t.Fatal(err)
	}
	waitForRuntime(t, service, record.Server.ID, func(state RuntimeState) bool { return state.CurrentState == StateStopped })
	if _, ok = manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("console remained attached after helper exit")
	}
}

func waitConsoleOutput(t *testing.T, session *console.Session, stream, data string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	events, done := session.Subscribe()
	defer done()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("console closed before receiving %s %q", stream, data)
			}
			if event.Stream == stream && strings.Contains(event.Data, data) {
				return
			}
		case <-deadline.C:
			t.Fatalf("did not receive %s %q", stream, data)
		}
	}
}

func TestWindowsConsoleIOHelper(t *testing.T) {
	if os.Getenv("GAMENODE_CONSOLE_SMOKE") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "stdout-ready")
	fmt.Fprintln(os.Stderr, "stderr-ready")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "quit" {
			return
		}
		fmt.Fprintf(os.Stdout, "echo:%s\n", scanner.Text())
	}
}
