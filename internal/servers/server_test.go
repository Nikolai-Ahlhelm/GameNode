package servers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/console"
	"gamenode/internal/database"
	"gamenode/internal/ports"
	"gamenode/internal/runtime"
)

type fakeRuntime struct {
	mu         sync.Mutex
	exits      chan runtime.ExitResult
	options    runtime.StartOptions
	start      func(runtime.StartOptions) error
	stop       func() error
	kill       func() error
	status     runtime.Status
	statusErr  error
	metrics    runtime.Metrics
	metricsErr error
	useStatus  bool
	starts     int
	stops      int
	kills      int
}

type testInput struct {
	strings.Builder
	closed int
}

func (i *testInput) Close() error { i.closed++; return nil }

var _ io.WriteCloser = (*testInput)(nil)

func (f *fakeRuntime) Start(_ context.Context, options runtime.StartOptions) (runtime.Identity, <-chan runtime.ExitResult, error) {
	f.mu.Lock()
	f.options = options
	f.starts++
	f.mu.Unlock()
	if f.start != nil {
		if err := f.start(options); err != nil {
			return runtime.Identity{}, nil, err
		}
	}
	f.mu.Lock()
	f.exits = make(chan runtime.ExitResult, 1)
	exits := f.exits
	f.mu.Unlock()
	f.mu.Lock()
	startKey := fmt.Sprintf("start-%d", f.starts)
	f.mu.Unlock()
	return runtime.Identity{PID: 123, StartKey: startKey}, exits, nil
}
func (f *fakeRuntime) Stop(context.Context, runtime.Identity, time.Duration) error {
	f.mu.Lock()
	f.stops++
	stop := f.stop
	f.mu.Unlock()
	if stop != nil {
		return stop()
	}
	f.exit(runtime.ExitResult{ExitCode: 0})
	return nil
}
func (f *fakeRuntime) Kill(context.Context, runtime.Identity) error {
	f.mu.Lock()
	f.kills++
	kill := f.kill
	f.mu.Unlock()
	if kill != nil {
		return kill()
	}
	f.exit(runtime.ExitResult{ExitCode: 1})
	return nil
}
func (f *fakeRuntime) exit(result runtime.ExitResult) {
	f.mu.Lock()
	exits := f.exits
	f.mu.Unlock()
	exits <- result
}
func (f *fakeRuntime) Status(context.Context, runtime.Identity) (runtime.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.useStatus {
		return f.status, f.statusErr
	}
	return runtime.Status{Running: true, Known: true}, nil
}
func (f *fakeRuntime) Metrics(context.Context, runtime.Identity) (runtime.Metrics, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.metrics, f.metricsErr
}
func testService(t *testing.T) (*Service, *fakeRuntime, *console.Manager, *sql.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{}
	manager := console.NewManager()
	return NewService(NewStore(db), f, manager), f, manager, db
}
func testServer(t *testing.T) Server {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Server{Name: "test", CreationMode: CreationCustom, WorkingDirectory: filepath.Dir(exe), Executable: exe, Arguments: []string{}, EnvironmentVariables: map[string]string{"GAME_NODE_TEST": "1"}, StopTimeoutSeconds: 1}
}

func TestServerValidationRejectsEscapingExecutable(t *testing.T) {
	dir := t.TempDir()
	server := Server{Name: "test", WorkingDirectory: dir, Executable: ".." + string(filepath.Separator) + "outside", StopTimeoutSeconds: 1}
	if err := server.Validate(); err == nil {
		t.Fatal("expected traversal validation failure")
	}
}
func TestServerValidationKeepsArgumentsStructured(t *testing.T) {
	server := testServer(t)
	server.Arguments = []string{"--name=hello world", "; not a shell command"}
	if err := server.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestLifecycleStateTransitions(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	record, err = service.Start(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Runtime.CurrentState != StateRunning || record.Runtime.PID != 123 {
		t.Fatalf("unexpected start state: %#v", record.Runtime)
	}
	record, err = service.Stop(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Runtime.CurrentState != StateStopped || record.Runtime.PID != 0 {
		t.Fatalf("unexpected stop state: %#v", record.Runtime)
	}
}

func TestPortPreflightBlocksExternalListenerBeforeRuntimeOrConsole(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err = service.ports.Add(context.Background(), record.Server.ID, ports.Port{Protocol: "tcp", BindAddress: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("external occupied port must be rejected on add")
	}
	// Insert the explicit assignment to exercise start preflight independently
	// from mutation-time validation.
	_, err = db.Exec("INSERT INTO server_ports(id,server_id,name,protocol,bind_address,port,created_at,updated_at) VALUES('p',?, '', 'tcp','127.0.0.1',?,?,?)", record.Server.ID, port, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err == nil {
		t.Fatal("start accepted externally occupied port")
	}
	f.mu.Lock()
	starts := f.starts
	f.mu.Unlock()
	if starts != 0 {
		t.Fatalf("runtime started %d times", starts)
	}
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("preflight created console session")
	}
}

func TestMonitoringCountersFinalizeOnlyOnceAndRestart(t *testing.T) {
	service, f, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.exit(runtime.ExitResult{ExitCode: 1, Err: errors.New("crashed")})
	crashed := waitForRuntime(t, service, record.Server.ID, func(state RuntimeState) bool { return state.CurrentState == StateCrashed })
	if crashed.Runtime.CrashCount != 1 || crashed.Runtime.LastExitAt == nil {
		t.Fatalf("crash monitoring state = %#v", crashed.Runtime)
	}
	// Calling the captured finalizer twice is idempotent and cannot double-count.
	instance, _ := service.instances.Load(record.Server.ID)
	if instance != nil {
		service.finalizeInstance(instance.(*processInstance), runtime.ExitResult{ExitCode: 1})
	}
	got, err := service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime.CrashCount != 1 {
		t.Fatalf("crash count = %d", got.Runtime.CrashCount)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Restart(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	got, err = service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime.RestartCount != 1 {
		t.Fatalf("restart count = %d", got.Runtime.RestartCount)
	}
}

func TestAutoRestartOnlySchedulesUnexpectedCrash(t *testing.T) {
	service, f, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.AutoRestartEnabled, server.AutoRestartMaxAttempts, server.AutoRestartWindowSeconds, server.AutoRestartDelaySeconds = true, 1, 60, 0
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.exit(runtime.ExitResult{ExitCode: 1, Err: errors.New("crashed")})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("auto restart was not scheduled")
		case <-poll.C:
		}
		f.mu.Lock()
		starts := f.starts
		f.mu.Unlock()
		if starts == 2 {
			break
		}
	}
	if _, err = service.Stop(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	starts := f.starts
	f.mu.Unlock()
	if starts != 2 {
		t.Fatalf("intentional stop caused restart: %d", starts)
	}
}
func TestDeleteTerminatesRunningServerAndPreservesWorkingDirectory(t *testing.T) {
	service, runtime, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.Delete(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	kills := runtime.kills
	runtime.mu.Unlock()
	if kills != 1 {
		t.Fatalf("delete kills = %d, want 1", kills)
	}
	if _, err = service.Get(context.Background(), record.Server.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted server lookup error = %v, want sql.ErrNoRows", err)
	}
	if info, statErr := os.Stat(record.Server.WorkingDirectory); statErr != nil || !info.IsDir() {
		t.Fatalf("working directory was removed: info=%v err=%v", info, statErr)
	}
}

func TestDeleteBlocksConcurrentStart(t *testing.T) {
	service, fake, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	killStarted := make(chan struct{})
	releaseKill := make(chan struct{})
	fake.kill = func() error {
		close(killStarted)
		<-releaseKill
		fake.exit(runtime.ExitResult{ExitCode: 1})
		return nil
	}
	deleted := make(chan error, 1)
	go func() { deleted <- service.Delete(context.Background(), record.Server.ID) }()
	<-killStarted
	if _, err = service.Start(context.Background(), record.Server.ID); err == nil || !strings.Contains(err.Error(), "deletion is in progress") {
		t.Fatalf("concurrent start error = %v", err)
	}
	close(releaseKill)
	if err = <-deleted; err != nil {
		t.Fatal(err)
	}
}

func TestRestartReturnsRunningState(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	record, err = service.Restart(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Runtime.CurrentState != StateRunning {
		t.Fatalf("restart state = %s", record.Runtime.CurrentState)
	}
}

func TestStartCreatesConsoleSessionAndWiresIO(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	session, ok := manager.CurrentSession(record.Server.ID)
	if !ok {
		t.Fatal("start did not create a console session")
	}
	if session.ID == "" || session.InstanceID == "" || session.ServerID != record.Server.ID {
		t.Fatalf("unexpected console session identity: %#v", session)
	}
	if _, err := f.options.IO.Stdout.Write([]byte("stdout")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.options.IO.Stderr.Write([]byte("stderr")); err != nil {
		t.Fatal(err)
	}
	input := &testInput{}
	f.options.IO.Stdin(input)
	if err := session.Input("command\n"); err != nil {
		t.Fatal(err)
	}
	if input.String() != "command\n" {
		t.Fatalf("stdin = %q", input.String())
	}
	events, done := session.Subscribe()
	defer done()
	first, second := <-events, <-events
	if first.Stream != "stdout" || first.Data != "stdout" || second.Stream != "stderr" || second.Data != "stderr" {
		t.Fatalf("unexpected output events: %#v, %#v", first, second)
	}
}

func TestStartFailureCleansOnlyItsOwnConsoleSession(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	input := &testInput{}
	var newer *console.Session
	f.start = func(options runtime.StartOptions) error {
		options.IO.Stdin(input)
		newer = manager.CreateSession(record.Server.ID, "newer-instance")
		return errors.New("start failed")
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err == nil {
		t.Fatal("start unexpectedly succeeded")
	}
	if input.closed == 0 {
		t.Fatal("cleanup did not close bound stdin")
	}
	current, ok := manager.CurrentSession(record.Server.ID)
	if !ok || current != newer {
		t.Fatal("stale cleanup removed the newer current session")
	}
	if got, ok := manager.Get(newer.ID); !ok || got != newer {
		t.Fatal("newer session was removed by stale cleanup")
	}
}

func waitForRuntime(t *testing.T, service *Service, id string, match func(RuntimeState) bool) Record {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		record, err := service.store.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if match(record.Runtime) {
			return record
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime state did not reach expected value")
	return Record{}
}

func TestProcessExitFinalizesConsoleSession(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	session, _ := manager.CurrentSession(record.Server.ID)
	input := &testInput{}
	f.options.IO.Stdin(input)
	f.exit(runtime.ExitResult{ExitCode: 0})
	final := waitForRuntime(t, service, record.Server.ID, func(state RuntimeState) bool { return state.CurrentState == StateStopped && state.PID == 0 })
	if final.Runtime.ExitCode == nil || *final.Runtime.ExitCode != 0 || final.Runtime.LastStopAt == nil {
		t.Fatalf("exit data not persisted: %#v", final.Runtime)
	}
	if input.closed != 1 {
		t.Fatalf("stdin close count = %d, want 1", input.closed)
	}
	if err := session.Input("after exit\n"); err == nil {
		t.Fatal("input was accepted after exit")
	}
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("exited session remained current")
	}
}

func TestStopKeepsConsoleUntilActualExitAndUsesFinalizer(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	session, _ := manager.CurrentSession(record.Server.ID)
	input := &testInput{}
	f.options.IO.Stdin(input)
	called := make(chan struct{})
	f.stop = func() error { close(called); return nil }
	result := make(chan error, 1)
	go func() { _, err := service.Stop(context.Background(), record.Server.ID); result <- err }()
	<-called
	if current, ok := manager.CurrentSession(record.Server.ID); !ok || current != session {
		t.Fatal("stop detached the console before process exit")
	}
	if err := session.Input("graceful input\n"); err != nil {
		t.Fatal(err)
	}
	f.exit(runtime.ExitResult{ExitCode: 0})
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if input.closed != 1 {
		t.Fatalf("stdin close count = %d, want 1", input.closed)
	}
}

func TestStdinStopFinalizesWhenTimeoutKillFindsProcessAlreadyExited(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.StopMethod = "stdin_command"
	server.StopCommand = "quit"
	server.StopTimeoutSeconds = 1
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	input := &testInput{}
	f.options.IO.Stdin(input)
	f.kill = func() error { return runtime.ErrNotRunning }
	stopped, err := service.Stop(context.Background(), record.Server.ID)
	if err != nil || stopped.Runtime.CurrentState != StateStopped || stopped.Runtime.PID != 0 {
		t.Fatalf("stop=%#v err=%v", stopped.Runtime, err)
	}
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("stale console session remained current")
	}
}

func TestStartReconcilesExitedActiveInstanceWhoseWaitCallbackIsDelayed(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.useStatus = true
	f.status = runtime.Status{Running: false, Known: true}
	f.mu.Unlock()
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatalf("reconciled start: %v", err)
	}
	if f.starts != 2 {
		t.Fatalf("starts=%d", f.starts)
	}
	if current, ok := manager.CurrentSession(record.Server.ID); !ok || current.InstanceID == "" {
		t.Fatal("new console session was not attached")
	}
}

func TestKillUsesCentralFinalizer(t *testing.T) {
	service, _, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Kill(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	final := waitForRuntime(t, service, record.Server.ID, func(state RuntimeState) bool { return state.CurrentState == StateStopped })
	if final.Runtime.ExitCode == nil || *final.Runtime.ExitCode != 1 {
		t.Fatalf("kill exit code not persisted: %#v", final.Runtime)
	}
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("killed session remained current")
	}
}

func TestFinalizeIsIdempotentAndStaleSafe(t *testing.T) {
	service, _, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	value, ok := service.instances.Load(record.Server.ID)
	if !ok {
		t.Fatal("active instance missing")
	}
	instance := value.(*processInstance)
	newer := manager.CreateSession(record.Server.ID, "newer-instance")
	newerState, err := service.store.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	newerState.Runtime.PID = 456
	newerState.Runtime.processStartKey = "newer-key"
	newerState.Runtime.CurrentState = StateRunning
	if err = service.store.SaveRuntime(context.Background(), record.Server.ID, newerState.Runtime); err != nil {
		t.Fatal(err)
	}
	service.finalizeInstance(instance, runtime.ExitResult{ExitCode: 1})
	service.finalizeInstance(instance, runtime.ExitResult{ExitCode: 1})
	if current, ok := manager.CurrentSession(record.Server.ID); !ok || current != newer {
		t.Fatal("stale finalizer removed newer current session")
	}
	got, err := service.store.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime.PID != 456 || got.Runtime.processStartKey != "newer-key" || got.Runtime.CurrentState != StateRunning {
		t.Fatalf("stale finalizer overwrote newer runtime state: %#v", got.Runtime)
	}
}

func TestStopAndKillFailuresDoNotFinalize(t *testing.T) {
	for name, configure := range map[string]func(*fakeRuntime){
		"stop": func(f *fakeRuntime) { f.stop = func() error { return errors.New("stop failed") } },
		"kill": func(f *fakeRuntime) { f.kill = func() error { return errors.New("kill failed") } },
	} {
		t.Run(name, func(t *testing.T) {
			service, f, manager, db := testService(t)
			defer db.Close()
			record, err := service.Create(context.Background(), testServer(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
				t.Fatal(err)
			}
			session, _ := manager.CurrentSession(record.Server.ID)
			input := &testInput{}
			f.options.IO.Stdin(input)
			configure(f)
			if name == "stop" {
				_, err = service.Stop(context.Background(), record.Server.ID)
			} else {
				_, err = service.Kill(context.Background(), record.Server.ID)
			}
			if err == nil {
				t.Fatal("lifecycle action unexpectedly succeeded")
			}
			if current, ok := manager.CurrentSession(record.Server.ID); !ok || current != session || input.closed != 0 {
				t.Fatal("failed lifecycle action finalized console session")
			}
		})
	}
}

func TestConcurrentLifecycleCallsRemainConsistent(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() { _, err := service.Stop(context.Background(), record.Server.ID); results <- err }()
	go func() { _, err := service.Kill(context.Background(), record.Server.ID); results <- err }()
	<-results
	<-results
	f.mu.Lock()
	actions := f.stops + f.kills
	f.mu.Unlock()
	if actions != 1 {
		t.Fatalf("runtime lifecycle actions = %d, want 1", actions)
	}
	waitForRuntime(t, service, record.Server.ID, func(state RuntimeState) bool { return state.CurrentState == StateStopped })
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("session remained current after concurrent lifecycle actions")
	}
}

func TestRestartWaitsForOldFinalizeAndCreatesFreshConsole(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	old, _ := manager.CurrentSession(record.Server.ID)
	oldValue, ok := service.instances.Load(record.Server.ID)
	if !ok {
		t.Fatal("old process instance missing")
	}
	oldInstance := oldValue.(*processInstance)
	oldInput := &testInput{}
	f.options.IO.Stdin(oldInput)
	stopped := make(chan struct{})
	f.stop = func() error { close(stopped); return nil }
	restarted := make(chan error, 1)
	go func() { _, err := service.Restart(context.Background(), record.Server.ID); restarted <- err }()
	<-stopped
	if current, ok := manager.CurrentSession(record.Server.ID); !ok || current != old {
		t.Fatal("restart replaced the console before old process finalized")
	}
	f.mu.Lock()
	startsBeforeExit := f.starts
	f.mu.Unlock()
	if startsBeforeExit != 1 {
		t.Fatalf("restart started a new process before old exit: %d starts", startsBeforeExit)
	}
	f.exit(runtime.ExitResult{ExitCode: 0})
	if err := <-restarted; err != nil {
		t.Fatal(err)
	}
	newSession, ok := manager.CurrentSession(record.Server.ID)
	if !ok || newSession == old || newSession.ID == old.ID || newSession.InstanceID == old.InstanceID {
		t.Fatal("restart did not create a distinct console session and instance")
	}
	if err := old.Input("old pipe\n"); err == nil {
		t.Fatal("old stdin remained usable after restart")
	}
	newInput := &testInput{}
	f.options.IO.Stdin(newInput)
	if err := newSession.Input("new pipe\n"); err != nil {
		t.Fatal(err)
	}
	if newInput.String() != "new pipe\n" {
		t.Fatalf("new stdin = %q", newInput.String())
	}
	// A duplicate, stale signal from the old process cannot mutate the new one.
	if value, ok := service.instances.Load(record.Server.ID); !ok || value.(*processInstance).session != newSession {
		t.Fatal("restarted process instance missing")
	}
	service.finalizeInstance(oldInstance, runtime.ExitResult{ExitCode: 1})
	if current, ok := manager.CurrentSession(record.Server.ID); !ok || current != newSession {
		t.Fatal("stale old finalizer affected restarted session")
	}
}

func TestRestartStartFailureLeavesStoppedFinalizedState(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	f.start = func(runtime.StartOptions) error {
		starts++
		if starts == 2 {
			return errors.New("replacement start failed")
		}
		return nil
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	old, _ := manager.CurrentSession(record.Server.ID)
	oldInput := &testInput{}
	f.options.IO.Stdin(oldInput)
	if _, err = service.Restart(context.Background(), record.Server.ID); err == nil {
		t.Fatal("restart unexpectedly succeeded")
	}
	final := waitForRuntime(t, service, record.Server.ID, func(state RuntimeState) bool { return state.CurrentState == StateStopped })
	if final.Runtime.PID != 0 || final.Runtime.LastError != "start failed" {
		t.Fatalf("restart failure left inconsistent runtime state: %#v", final.Runtime)
	}
	if oldInput.closed != 1 || old.Input("old pipe\n") == nil {
		t.Fatal("old session was not finalized before replacement start failure")
	}
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("failed replacement left a current console session")
	}
}

func TestRestartStopFailureKeepsExistingConsole(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := manager.CurrentSession(record.Server.ID)
	f.stop = func() error { return errors.New("stop failed") }
	if _, err = service.Restart(context.Background(), record.Server.ID); err == nil {
		t.Fatal("restart unexpectedly succeeded")
	}
	if got, ok := manager.CurrentSession(record.Server.ID); !ok || got != current {
		t.Fatal("restart stop failure finalized or replaced console session")
	}
}

func TestRediscoveryMarksVerifiedProcessDetachedWithoutSession(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
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
	f.mu.Unlock()
	if err = service.Rediscover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !manager.IsDetached(record.Server.ID) {
		t.Fatal("verified rediscovery was not marked detached")
	}
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("rediscovery created an attached session")
	}
	if _, ok := manager.Get(record.Server.ID); ok {
		t.Fatal("rediscovery created a fake console session")
	}
}

func TestInvalidRediscoveryDoesNotDetachAndManagedStartClearsDetached(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := service.store.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Runtime.PID = 321
	persisted.Runtime.processStartKey = "invalid"
	persisted.Runtime.CurrentState = StateRunning
	if err = service.store.SaveRuntime(context.Background(), record.Server.ID, persisted.Runtime); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.useStatus = true
	f.status = runtime.Status{Known: false}
	f.mu.Unlock()
	if err = service.Rediscover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.IsDetached(record.Server.ID) {
		t.Fatal("invalid rediscovery was marked detached")
	}
	manager.MarkDetached(record.Server.ID)
	persisted, err = service.store.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Runtime.CurrentState = StateStopped
	persisted.Runtime.PID = 0
	persisted.Runtime.processStartKey = ""
	if err = service.store.SaveRuntime(context.Background(), record.Server.ID, persisted.Runtime); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	if manager.IsDetached(record.Server.ID) {
		t.Fatal("managed start did not clear detached state")
	}
	if _, ok := manager.CurrentSession(record.Server.ID); !ok {
		t.Fatal("managed start did not create an attached session")
	}
}

func TestRediscoveryStatusErrorDoesNotRetainRunningOrAllowDuplicateStart(t *testing.T) {
	service, f, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := service.store.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted.Runtime.PID = 321
	persisted.Runtime.processStartKey = "unverified"
	persisted.Runtime.CurrentState = StateRunning
	if err = service.store.SaveRuntime(context.Background(), record.Server.ID, persisted.Runtime); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.useStatus = true
	f.statusErr = errors.New("process query failed")
	f.mu.Unlock()

	got, err := service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime.CurrentState != StateUnknown || got.Runtime.PID != 321 || got.Runtime.LastError != "process status could not be verified" {
		t.Fatalf("unexpected unverified state: %#v", got.Runtime)
	}
	if manager.IsDetached(record.Server.ID) {
		t.Fatal("status-error rediscovery was marked detached")
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err == nil {
		t.Fatal("unverified process allowed a duplicate start")
	}
}
