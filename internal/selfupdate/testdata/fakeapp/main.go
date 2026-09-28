// Command fakeapp is a stand-in for GameNode used only by the selfupdate
// end-to-end test. It is built several times with different -X main.version
// values so real binaries can update and roll back each other as real
// processes. It uses the real selfupdate.Service, the real --version
// self-test, the real executable swap, and the real relaunch.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gamenode/internal/selfupdate"
)

var (
	version      = "0.0.0"
	crashOnStart = "" // "1": exit immediately after Boot, simulating an unhealthy release
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Printf("gamenode %s\n", version)
		return
	}
	if hook := run(); hook != nil {
		if err := hook(); err != nil {
			fmt.Fprintln(os.Stderr, "relaunch failed:", err)
			os.Exit(1)
		}
	}
}

type dirSource struct{ dir string }

func (d dirSource) Latest(context.Context) (selfupdate.Release, error) {
	var release selfupdate.Release
	data, err := os.ReadFile(filepath.Join(d.dir, "release.json"))
	if err != nil {
		return release, err
	}
	return release, json.Unmarshal(data, &release)
}

func (d dirSource) Open(_ context.Context, _ selfupdate.Release, asset string, _ int64) (io.ReadCloser, int64, error) {
	file, err := os.Open(filepath.Join(d.dir, asset))
	if err != nil {
		return nil, 0, err
	}
	info, _ := file.Stat()
	return file, info.Size(), nil
}

func run() func() error {
	data, releaseDir := os.Getenv("FAKEAPP_DATA"), os.Getenv("FAKEAPP_RELEASE")
	event := func(format string, args ...any) {
		file, err := os.OpenFile(filepath.Join(data, "events.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer file.Close()
		fmt.Fprintf(file, format+"\n", args...)
	}
	updater, err := selfupdate.New(selfupdate.Options{
		DataDirectory: data, CurrentVersion: version, Source: dirSource{releaseDir}, Args: os.Args[1:],
		ConfirmAfter: 400 * time.Millisecond, RestartDelay: 100 * time.Millisecond,
		Activity:       func(context.Context) (selfupdate.Activity, error) { return selfupdate.Activity{}, nil },
		DatabaseCheck:  func(context.Context) error { return nil },
		DatabaseBackup: func(_ context.Context, path string) error { return os.WriteFile(path, []byte("db"), 0o600) },
	})
	if err != nil {
		event("fatal %v", err)
		os.Exit(2)
	}
	if boot := updater.Boot(); boot.Relaunch {
		event("rollback-relaunch from=%s", version)
		return updater.Relaunch
	}
	event("start %s", version)
	if crashOnStart == "1" {
		os.Exit(1)
	}
	updater.Confirm(func(outcome selfupdate.Outcome) {
		event("outcome %s %s->%s actor=%s", outcome.Result, outcome.From, outcome.To, outcome.ActorUsername)
	})
	sentinel := filepath.Join(data, "update-attempted")
	if _, statErr := os.Stat(sentinel); statErr != nil && os.Getenv("FAKEAPP_ACTION") == "update" {
		ctx := context.Background()
		status, _ := updater.Check(ctx)
		if status.UpdateAvailable {
			_ = os.WriteFile(sentinel, nil, 0o600)
			if err := updater.Prepare(ctx, status.Available.Version); err != nil {
				event("prepare-failed %v", err)
				os.Exit(3)
			}
			for deadline := time.Now().Add(30 * time.Second); updater.Status(ctx).State != selfupdate.StateReady; time.Sleep(20 * time.Millisecond) {
				if updater.Status(ctx).State == selfupdate.StateFailed || time.Now().After(deadline) {
					event("download-failed %+v", updater.Status(ctx).Error)
					os.Exit(3)
				}
			}
			if err := updater.Apply(ctx, status.Available.Version, true, selfupdate.Actor{ID: "u1", Username: "admin"}); err != nil {
				event("apply-failed %v", err)
				os.Exit(3)
			}
			event("applied %s", status.Available.Version)
			<-updater.RestartRequested()
			return updater.Relaunch
		}
	}
	time.Sleep(2 * time.Second) // long enough for the confirmation timer
	event("exit %s", version)
	return nil
}
