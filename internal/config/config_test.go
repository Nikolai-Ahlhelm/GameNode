package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateCreatesUsableDefaultConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	got, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	wantData := filepath.Join(filepath.Dir(path), "data")
	if got.Server.Listen != "127.0.0.1:8443" || got.Data.Directory != wantData || got.Database.Path != filepath.Join(wantData, "gamenode.db") {
		t.Fatalf("LoadOrCreate() = %#v", got)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if len(contents) == 0 {
		t.Fatal("generated config is empty")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load(generated config) error = %v", err)
	}
	if loaded.Server.Listen != got.Server.Listen || loaded.Database.Path != got.Database.Path {
		t.Fatalf("loaded config = %#v, want %#v", loaded, got)
	}
}

func TestLoadOrCreatePreservesExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const contents = "server:\n  listen: 127.0.0.1:9443\ndatabase:\n  path: ./custom.db\nfilesystem:\n  max_upload_bytes: 1048576\nmonitoring:\n  sample_interval_seconds: 5\n  history_limit: 300\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	if got.Server.Listen != "127.0.0.1:9443" {
		t.Fatalf("listen = %q", got.Server.Listen)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != contents {
		t.Fatalf("existing config was changed: %q", after)
	}
}

func TestFileSetStoragePersistsAbsolutePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	initial, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "managed-data")
	database := filepath.Join(data, "state", "gamenode.db")
	file := NewFile(path, initial)
	if err = file.SetStorage(data, database); err != nil {
		t.Fatalf("SetStorage() error = %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Data.Directory != data || loaded.Database.Path != database {
		t.Fatalf("storage = %q, %q", loaded.Data.Directory, loaded.Database.Path)
	}
	if err = file.SetStorage("relative", database); err == nil {
		t.Fatal("relative data directory accepted")
	}
}

func TestUpdateRestartModeDefaultsAndValidates(t *testing.T) {
	dir := t.TempDir()
	write := func(name, extra string) string {
		path := filepath.Join(dir, name)
		contents := "server:\n  listen: 127.0.0.1:9443\ndatabase:\n  path: ./custom.db\n" + extra
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// An existing configuration without an update section keeps working.
	loaded, err := Load(write("legacy.yaml", ""))
	if err != nil || loaded.Update.RestartMode != "self" {
		t.Fatalf("legacy config: %+v %v", loaded.Update, err)
	}
	loaded, err = Load(write("exit.yaml", "update:\n  restart_mode: exit\n"))
	if err != nil || loaded.Update.RestartMode != "exit" {
		t.Fatalf("exit mode: %+v %v", loaded.Update, err)
	}
	for _, bad := range []string{"restart", "", "shell", "SELF"} {
		if _, err := Load(write("bad.yaml", "update:\n  restart_mode: \""+bad+"\"\n")); err == nil {
			t.Errorf("restart_mode %q must be rejected", bad)
		}
	}
}
