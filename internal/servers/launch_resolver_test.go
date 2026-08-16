package servers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubResolver struct {
	calls     int
	arguments []string
	err       error
}

func (r *stubResolver) ResolveLaunch(_ context.Context, _ string, arguments []string, environment map[string]string) ([]string, map[string]string, error) {
	r.calls++
	if r.err != nil {
		return nil, nil, r.err
	}
	r.arguments = arguments
	resolved := append(append([]string{}, arguments...), "-name", "My Valheim")
	merged := map[string]string{"GAME_TOKEN": "resolved"}
	for key, value := range environment {
		merged[key] = value
	}
	return resolved, merged, nil
}

// TestStartUsesResolvedLaunchWithoutPersistingIt proves the managed launch
// reaches the runtime while the persisted server definition stays unchanged.
func TestStartUsesResolvedLaunchWithoutPersistingIt(t *testing.T) {
	service, fake, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.Arguments = []string{"-nographics", "-batchmode"}
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &stubResolver{}
	service.SetLaunchResolver(resolver)
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	options := fake.options
	fake.mu.Unlock()
	if resolver.calls != 1 {
		t.Fatalf("resolver call count=%d", resolver.calls)
	}
	if strings.Join(options.Arguments, " ") != "-nographics -batchmode -name My Valheim" {
		t.Fatalf("runtime did not receive the resolved launch: %#v", options.Arguments)
	}
	if options.Environment["GAME_TOKEN"] != "resolved" || options.Environment["GAME_NODE_TEST"] != "1" {
		t.Fatalf("runtime did not receive the resolved environment: %#v", options.Environment)
	}
	stored, err := service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(stored.Server.Arguments, " ") != "-nographics -batchmode" {
		t.Fatalf("resolved launch was persisted: %#v", stored.Server.Arguments)
	}
	if _, ok := stored.Server.EnvironmentVariables["GAME_TOKEN"]; ok {
		t.Fatal("resolved environment was persisted")
	}
}

// TestStartFailsClosedOnUnresolvableConfiguration keeps an incomplete managed
// configuration from starting a differently configured game process.
func TestStartFailsClosedOnUnresolvableConfiguration(t *testing.T) {
	service, fake, manager, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	service.SetLaunchResolver(&stubResolver{err: errors.New("SERVER_NAME is not configured")})
	if _, err = service.Start(context.Background(), record.Server.ID); err == nil {
		t.Fatal("start must fail when managed configuration cannot be resolved")
	}
	fake.mu.Lock()
	starts := fake.starts
	fake.mu.Unlock()
	if starts != 0 {
		t.Fatalf("no process may be started: %d", starts)
	}
	if _, ok := manager.CurrentSession(record.Server.ID); ok {
		t.Fatal("no console session may be created")
	}
	current, err := service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Runtime.CurrentState != StateStopped || !strings.Contains(current.Runtime.LastError, "SERVER_NAME") {
		t.Fatalf("unexpected state: %#v", current.Runtime)
	}
}

// TestStartWithoutResolverIsUnchanged protects servers that have no managed
// configuration at all.
func TestStartWithoutResolverIsUnchanged(t *testing.T) {
	service, fake, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.Arguments = []string{"-only", "-base"}
	record, err := service.Create(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	options := fake.options
	fake.mu.Unlock()
	if strings.Join(options.Arguments, " ") != "-only -base" {
		t.Fatalf("unexpected arguments: %#v", options.Arguments)
	}
}
