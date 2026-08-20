package ftp

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"gamenode"
	"gamenode/internal/database"
	"gamenode/internal/filesystem"
)

func ftpTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertFTPServer(t *testing.T, db *sql.DB, id, name, root string) {
	t.Helper()
	const stamp = "2026-01-01T00:00:00.000000000Z"
	_, err := db.Exec(`INSERT INTO servers(id,tenant_id,creation_mode,name,description,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,auto_restart_enabled,auto_restart_max_attempts,auto_restart_window_seconds,auto_restart_delay_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "default", "custom", name, "", root, "game.exe", "[]", "{}", "native", 0, "never", "terminate", "", 15, 0, 3, 300, 5, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRotateAuthenticatesAndRevokesPerServer(t *testing.T) {
	db := ftpTestDB(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	serverID := "11111111-1111-4111-a111-111111111111"
	insertFTPServer(t, db, serverID, "Alpha", root)
	service, err := New(db, filesystem.New(), Options{Enabled: true, ListenAddr: "127.0.0.1:2121"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := service.Profile(context.Background(), serverID)
	if err != nil || profile.Configured || profile.Enabled || profile.Username == "" {
		t.Fatalf("unexpected initial profile: %#v, %v", profile, err)
	}
	credential, err := service.Rotate(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Password == "" || !credential.Enabled || !credential.Configured {
		t.Fatalf("rotation did not enable credential: %#v", credential)
	}
	resolved, err := service.authenticate(context.Background(), credential.Username, credential.Password)
	if err != nil || resolved != root {
		t.Fatalf("authenticate: root=%q err=%v", resolved, err)
	}
	if _, err = service.SetEnabled(context.Background(), serverID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = service.authenticate(context.Background(), credential.Username, credential.Password); err == nil {
		t.Fatal("disabled credential authenticated")
	}
}

func TestOverlappingRootsCannotBeEnabled(t *testing.T) {
	db := ftpTestDB(t)
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	insertFTPServer(t, db, "22222222-2222-4222-a222-222222222222", "Parent", parent)
	childID := "33333333-3333-4333-a333-333333333333"
	insertFTPServer(t, db, childID, "Child", child)
	service, err := New(db, filesystem.New(), Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Rotate(context.Background(), childID); err != ErrRootOverlap {
		t.Fatalf("overlap error = %v, want %v", err, ErrRootOverlap)
	}
}
