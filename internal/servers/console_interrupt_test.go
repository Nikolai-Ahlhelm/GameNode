package servers

import (
	"context"
	"errors"
	"testing"
	"time"

	"gamenode/internal/runtime"
)

// These tests cover CONSOLE-INTERRUPT.md section 14 "Server Lifecycle Tests"
// using fakeRuntime, so they run on every platform even though the compiled
// console_interrupt stop type is Windows-only in production. They verify
// internal/servers' own orchestration decisions (which Runtime method is
// called, when, and how failures are classified), not the OS-level Windows
// console control event itself; that is covered by
// internal/runtime/console_interrupt_windows_test.go.

func TestConsoleInterruptValidationAcceptsMethodAndRejectsStopCommand(t *testing.T) {
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	if err := server.Validate(); err != nil {
		t.Fatalf("console_interrupt should validate cleanly: %v", err)
	}
	withCommand := testServer(t)
	withCommand.StopMethod = StopMethodConsoleInterrupt
	withCommand.StopCommand = "stop"
	if err := withCommand.Validate(); err == nil {
		t.Fatal("console_interrupt with a stop command must be rejected")
	}
}

func TestConsoleInterruptUnknownStopMethodRejected(t *testing.T) {
	server := testServer(t)
	server.StopMethod = "sigterm"
	if err := server.Validate(); err == nil {
		t.Fatal("unknown stop method must be rejected")
	}
}

func TestConsoleInterruptStartSetsCapabilityFlagOnlyForThatMethod(t *testing.T) {
	service, f, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	capable := f.options.ConsoleInterruptCapable
	f.mu.Unlock()
	if !capable {
		t.Fatal("console_interrupt server did not request a console-interrupt-capable start")
	}

	// An ordinary terminate server must be completely unaffected.
	ordinaryServer := testServer(t)
	ordinaryServer.Name = "ordinary-terminate"
	ordinary, err := service.Create(context.Background(), ordinaryServer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), ordinary.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	capable = f.options.ConsoleInterruptCapable
	f.mu.Unlock()
	if capable {
		t.Fatal("terminate server unexpectedly requested a console-interrupt-capable start")
	}
}

func TestConsoleInterruptStopCallsRuntimeInterruptNotStopOrKill(t *testing.T) {
	service, f, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	stopped, err := service.Stop(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	interrupts, stops, kills := f.interrupts, f.stops, f.kills
	f.mu.Unlock()
	if interrupts != 1 || stops != 0 || kills != 0 {
		t.Fatalf("interrupts=%d stops=%d kills=%d, want 1/0/0", interrupts, stops, kills)
	}
	if stopped.Runtime.CurrentState != StateStopped || stopped.Runtime.PID != 0 {
		t.Fatalf("unexpected stop state: %#v", stopped.Runtime)
	}
}

func TestConsoleInterruptKillRemainsImmediateAndBypassesInterrupt(t *testing.T) {
	service, f, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Kill(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	interrupts, kills := f.interrupts, f.kills
	f.mu.Unlock()
	if kills != 1 || interrupts != 0 {
		t.Fatalf("kills=%d interrupts=%d, want 1/0", kills, interrupts)
	}
}

// TestConsoleInterruptTimeoutFallsBackToForceKill covers section 12 "Timeout":
// a signal that is delivered but ignored must escalate to the existing
// bounded Kill path, exactly like stdin_command already does.
func TestConsoleInterruptTimeoutFallsBackToForceKill(t *testing.T) {
	service, f, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	server.StopTimeoutSeconds = 1
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	// A real console interrupt only sends a signal; it does not itself wait.
	// Simulate the process ignoring it by not exiting.
	f.interrupt = func() error { return nil }
	stopped, err := service.Stop(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	interrupts, kills := f.interrupts, f.kills
	f.mu.Unlock()
	if interrupts != 1 || kills != 1 {
		t.Fatalf("interrupts=%d kills=%d, want 1/1 after timeout fallback", interrupts, kills)
	}
	if stopped.Runtime.CurrentState != StateStopped {
		t.Fatalf("unexpected final state after force-kill fallback: %#v", stopped.Runtime)
	}
}

// TestConsoleInterruptTimeoutFallbackFindsAlreadyExited mirrors the existing
// stdin_command race where the process accepted the signal and exited but
// the Wait callback is delayed by inherited Windows pipe handles, so the
// force-kill attempt discovers the identity already gone.
func TestConsoleInterruptTimeoutFallbackFindsAlreadyExited(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	server.StopTimeoutSeconds = 1
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.interrupt = func() error { return nil }
	f.kill = func() error { return runtime.ErrNotRunning }
	stopped, err := service.Stop(context.Background(), record.Server.ID)
	if err != nil || stopped.Runtime.CurrentState != StateStopped || stopped.Runtime.PID != 0 {
		t.Fatalf("stop=%#v err=%v", stopped.Runtime, err)
	}
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("stale console session remained current")
	}
}

// TestConsoleInterruptRediscoveredFallsBackToTerminate covers section 8/12
// "Rediscovery": a process identity that survived a GameNode restart has no
// in-memory instance and must not receive a claimed graceful interrupt; the
// existing terminate lifecycle is used instead.
func TestConsoleInterruptRediscoveredFallsBackToTerminate(t *testing.T) {
	service, f, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := service.store.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Runtime.PID = 321
	persisted.Runtime.processStartKey = "rediscovered"
	persisted.Runtime.CurrentState = StateRunning
	if err = service.store.SaveRuntime(context.Background(), record.Server.ID, persisted.Runtime); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.useStatus = true
	f.status = runtime.Status{Running: true, Known: true}
	// This identity was never started through this fakeRuntime instance (it
	// simulates surviving a GameNode restart), so its exits channel was never
	// initialized. The rediscovered/detached fallback path never waits on it;
	// avoid sending on a nil channel by not calling the default Stop hook.
	f.stop = func() error { return nil }
	f.mu.Unlock()

	if _, err = service.Stop(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	interrupts, stops := f.interrupts, f.stops
	// A rediscovered identity has no in-memory instance to wait on, so
	// signalWithRestart returns immediately after the terminate call
	// succeeds, leaving "stopping" persisted until the next opportunistic
	// refresh reconciles it against verified runtime status — exactly the
	// same behavior already relied on for a rediscovered "terminate"/"kill".
	// Reflect that the simulated process is now actually gone before that
	// reconciliation runs.
	f.status = runtime.Status{Running: false, Known: true}
	f.mu.Unlock()
	if interrupts != 0 || stops != 1 {
		t.Fatalf("interrupts=%d stops=%d, want 0/1 for a rediscovered identity", interrupts, stops)
	}
	final, err := service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Runtime.CurrentState != StateStopped {
		t.Fatalf("unexpected rediscovered stop state after reconciliation: %#v", final.Runtime)
	}
}

// TestConsoleInterruptManualStopIsNotCrashAndDoesNotAutoRestart covers
// section 14: a user-initiated console_interrupt stop must not be treated as
// a crash and must not trigger auto-restart, exactly like every other stop
// method.
func TestConsoleInterruptManualStopIsNotCrashAndDoesNotAutoRestart(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	server.AutoRestartEnabled = true
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	stopped, err := service.Stop(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Runtime.CurrentState != StateStopped {
		t.Fatalf("manual console_interrupt stop must not be crashed: %#v", stopped.Runtime)
	}
	if _, pending := service.autoRestarts.Load(record.Server.ID); pending {
		t.Fatal("manual stop must not schedule an automatic restart")
	}
	time.Sleep(50 * time.Millisecond)
	final, err := service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Runtime.CurrentState != StateStopped {
		t.Fatalf("no delayed auto-restart should have started a new instance: %#v", final.Runtime)
	}
}

// TestConsoleInterruptRestartWaitsForFullFinalization covers section 14
// "Restart wartet auf vollständige Finalisierung".
func TestConsoleInterruptRestartWaitsForFullFinalization(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	old, _ := manager.CurrentSession(record.Server.ID)
	restarted, err := service.Restart(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Runtime.CurrentState != StateRunning {
		t.Fatalf("restart did not end running: %#v", restarted.Runtime)
	}
	current, ok := manager.CurrentSession(record.Server.ID)
	if !ok || current.ID == old.ID {
		t.Fatal("restart did not replace the console session with a fresh one")
	}
	f.mu.Lock()
	interrupts := f.interrupts
	f.mu.Unlock()
	if interrupts != 1 {
		t.Fatalf("restart should stop through exactly one interrupt, got %d", interrupts)
	}
}

// TestConsoleInterruptSendFailureLeavesStateUnknownAndDoesNotFinalize
// exercises the "Signal API schlägt fehl" scenario from section 12: a failed
// send must not be reported as success and must not finalize the still-live
// instance twice.
func TestConsoleInterruptSendFailureLeavesStateUnknownAndDoesNotFinalize(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = StopMethodConsoleInterrupt
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	session, _ := manager.CurrentSession(record.Server.ID)
	f.interrupt = func() error { return runtime.ErrConsoleInterruptFailed }
	if _, err = service.Stop(context.Background(), record.Server.ID); !errors.Is(err, runtime.ErrConsoleInterruptFailed) {
		t.Fatalf("expected ErrConsoleInterruptFailed, got %v", err)
	}
	final, err := service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Runtime.CurrentState != StateUnknown {
		t.Fatalf("failed signal delivery should leave state unknown, got %#v", final.Runtime)
	}
	if current, ok := manager.CurrentSession(record.Server.ID); !ok || current != session {
		t.Fatal("a failed signal send must not finalize or replace the live console session")
	}
}
