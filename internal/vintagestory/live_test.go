package vintagestory

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveInstallLaunchAndStop installs the newest stable server from the real
// official source, launches it exactly as GameNode would (resolved dotnet,
// root-relative arguments, working directory = root), waits for it to accept
// players, then stops it with the configured /stop console command. It needs
// network access and the matching .NET runtime and is opt-in:
//
//	GAMENODE_VINTAGESTORY_LIVE=1 go test ./internal/vintagestory -run Live -v
func TestLiveInstallLaunchAndStop(t *testing.T) {
	if os.Getenv("GAMENODE_VINTAGESTORY_LIVE") != "1" {
		t.Skip("set GAMENODE_VINTAGESTORY_LIVE=1 to run against the real official source")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	source := NewSource()
	versions, err := source.Versions(ctx)
	if err != nil || len(versions) == 0 {
		t.Fatalf("versions: %v %v", versions, err)
	}
	var plan Plan
	for _, version := range versions {
		if version.Latest {
			plan = Plan{Version: version.Version}
		}
	}
	root := t.TempDir()
	if err = NewInstaller(source).Install(ctx, root, plan, io.Discard, func(e Event) { t.Log(e.Phase, e.Summary) }); err != nil {
		t.Fatalf("install %+v: %v", plan, err)
	}
	launch, err := ResolveLaunch(root, 42997)
	if err != nil || !launch.DotnetFound {
		t.Fatalf("resolve: %+v %v", launch, err)
	}
	t.Logf("%s %v", launch.Executable, launch.Arguments)
	command := exec.CommandContext(ctx, launch.Executable, launch.Arguments...)
	command.Dir = launch.WorkingDirectory
	stdin, _ := command.StdinPipe()
	stdout, _ := command.StdoutPipe()
	command.Stderr = command.Stdout
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "Dedicated Server now running on Port 42997") {
				ready <- true
				io.Copy(io.Discard, stdout)
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("the server exited before it started accepting players")
		}
	case <-time.After(5 * time.Minute):
		command.Process.Kill()
		t.Fatal("the server did not start within 5 minutes")
	}
	if _, err = os.Stat(filepath.Join(root, "data", "serverconfig.json")); err != nil {
		t.Fatalf("a root-relative --dataPath must create data/serverconfig.json inside the root: %v", err)
	}
	stop := launch.StopCommand + "\n"
	if launch.ConsoleLineEnding == "crlf" {
		stop = launch.StopCommand + "\r\n"
	}
	io.WriteString(stdin, stop)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		command.Process.Kill()
		t.Fatal("the server did not stop within the configured timeout after the /stop command")
	}
}
