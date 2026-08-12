package steamcmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestManagedBootstrapIntegration is deliberately opt-in: the regular unit
// suite never reaches the network or installs an application.
func TestManagedBootstrapIntegration(t *testing.T) {
	if os.Getenv("GAMENODE_STEAMCMD_INTEGRATION") != "1" {
		t.Skip("set GAMENODE_STEAMCMD_INTEGRATION=1 to exercise the official SteamCMD bootstrap")
	}
	platform, err := CurrentPlatform(runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	manager := New(filepath.Join(t.TempDir(), "tools", "steamcmd"), platform, nil, nil)
	if err := manager.Ensure(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !manager.Detect() {
		t.Fatal("managed SteamCMD was not detected after bootstrap")
	}
	if err := manager.Ensure(ctx, nil); err != nil {
		t.Fatalf("second managed bootstrap did not reuse the installation: %v", err)
	}
}

// Test7DaysToDieInstallIntegration performs the large, explicitly opt-in
// acceptance download. It validates the native SteamCMD manager only; host
// launch provisionability remains the provisioning service's responsibility.
func Test7DaysToDieInstallIntegration(t *testing.T) {
	data := os.Getenv("GAMENODE_7DTD_ACCEPTANCE_DATA")
	if data == "" {
		t.Skip("set GAMENODE_7DTD_ACCEPTANCE_DATA to an empty isolated GameNode data directory")
	}
	data, err := filepath.Abs(data)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(data, "servers", "7dtd-real-download")
	if entries, readErr := os.ReadDir(target); readErr == nil && len(entries) != 0 {
		t.Fatal("acceptance target is not empty")
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if err = os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	platform, err := CurrentPlatform(runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(filepath.Join(data, "tools", "steamcmd"), platform, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	if err = manager.Install(ctx, target, InstallPlan{AppID: 294420, Validate: true, LoginMode: "anonymous"}, io.Discard, func(event Event) {
		t.Logf("%s: %s", event.Phase, event.Summary)
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) == 0 {
		t.Fatalf("7DTD target was not populated: entries=%d err=%v", len(entries), err)
	}
	if _, err = os.Stat(filepath.Join(target, "steamapps", "appmanifest_294420.acf")); err != nil {
		t.Fatalf("SteamCMD app manifest missing: %v", err)
	}
}

// TestProjectZomboidInstallIntegration performs the explicitly opt-in real
// Project Zomboid Dedicated Server download. Keep this separate from the
// regular suite so CI never depends on Steam or several gigabytes of disk.
func TestProjectZomboidInstallIntegration(t *testing.T) {
	data := os.Getenv("GAMENODE_PZ_ACCEPTANCE_DATA")
	if data == "" {
		t.Skip("set GAMENODE_PZ_ACCEPTANCE_DATA to an empty isolated GameNode data directory")
	}
	data, err := filepath.Abs(data)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(data, "servers", "project-zomboid-real-download")
	if entries, readErr := os.ReadDir(target); readErr == nil && len(entries) != 0 {
		// SteamCMD keeps resumable download state below steamapps. Refuse every
		// unrelated pre-existing target so an opt-in smoke cannot overwrite a
		// real server directory. A completed installation can be revalidated.
		manifest, manifestErr := os.ReadFile(filepath.Join(target, "steamapps", "appmanifest_380870.acf"))
		resumableOnly := len(entries) == 1 && entries[0].Name() == "steamapps" && entries[0].IsDir()
		installedPZ := manifestErr == nil && bytes.Contains(manifest, []byte("\"appid\"\t\t\"380870\""))
		if !resumableOnly && !installedPZ {
			t.Fatal("acceptance target is not an isolated Project Zomboid installation")
		}
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if err = os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	platform, err := CurrentPlatform(runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(filepath.Join(data, "tools", "steamcmd"), platform, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	logFile, err := os.OpenFile(filepath.Join(data, "project-zomboid-steamcmd.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	if err = manager.Install(ctx, target, InstallPlan{AppID: 380870, Validate: true, LoginMode: "anonymous"}, logFile, func(event Event) {
		t.Logf("%s: %s", event.Phase, event.Summary)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(target, "steamapps", "appmanifest_380870.acf")); err != nil {
		t.Fatalf("SteamCMD app manifest missing: %v", err)
	}
	if runtime.GOOS == "windows" {
		for _, artifact := range []string{"StartServer64.bat", "ProjectZomboid64.json", filepath.Join("jre64", "bin", "java.exe")} {
			if _, err = os.Stat(filepath.Join(target, artifact)); err != nil {
				t.Fatalf("expected Windows artifact %s missing: %v", artifact, err)
			}
		}
	}
	if runtime.GOOS == "linux" {
		if _, err = os.Stat(filepath.Join(target, "start-server.sh")); err != nil {
			t.Fatalf("expected Linux artifact missing: %v", err)
		}
	}
}
