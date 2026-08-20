package runtime

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeContainerEngine struct {
	mu      sync.Mutex
	id      string
	running bool
	options ContainerOptions
}

func (f *fakeContainerEngine) Available(context.Context) error { return nil }
func (f *fakeContainerEngine) Create(_ context.Context, options ContainerOptions, _ StartOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.options = options
	return f.id, nil
}
func (f *fakeContainerEngine) Start(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = true
	return nil
}
func (f *fakeContainerEngine) Stop(context.Context, string, time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	return nil
}
func (f *fakeContainerEngine) Kill(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	return nil
}
func (f *fakeContainerEngine) Inspect(context.Context, string) (containerInspection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return containerInspection{Running: f.running, Known: true, Labels: map[string]string{"io.gamenode.managed": "true", "io.gamenode.server_id": f.options.ServerID, "io.gamenode.instance_generation": f.options.Generation, "io.gamenode.ownership_token": f.options.OwnershipToken}}, nil
}
func (f *fakeContainerEngine) Stats(context.Context, string) (Metrics, error) {
	return Metrics{MemoryBytes: 42}, nil
}
func (f *fakeContainerEngine) Attach(context.Context, string) (io.ReadWriteCloser, error) {
	client, _ := net.Pipe()
	return client, nil
}
func (f *fakeContainerEngine) ImageAvailable(context.Context, string) (bool, error) { return true, nil }
func (f *fakeContainerEngine) PullImage(context.Context, string) error              { return nil }

func TestContainerRuntimeUsesConcreteEngineIdentity(t *testing.T) {
	engine := &fakeContainerEngine{id: "engine-container-id"}
	r := NewContainer(engine)
	id, _, err := r.Start(context.Background(), StartOptions{RuntimeType: "container", WorkingDirectory: "/srv/game", Container: &ContainerOptions{Image: "example/game:1", MemoryLimitBytes: 64 << 20, CPULimitMillis: 1000, ServerID: "server", Generation: "generation", OwnershipToken: "owner"}})
	if err != nil {
		t.Fatal(err)
	}
	if id.ContainerID != "engine-container-id" || containerID(id) != "engine-container-id" {
		t.Fatalf("identity=%#v", id)
	}
	if engine.options.ServerID != "server" || engine.options.Generation != "generation" {
		t.Fatalf("ownership options=%#v", engine.options)
	}
	if err = r.Stop(context.Background(), id, time.Second); err != nil {
		t.Fatal(err)
	}
}
