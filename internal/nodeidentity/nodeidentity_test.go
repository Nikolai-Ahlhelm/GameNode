package nodeidentity

import (
	"context"
	"database/sql"
	"testing"

	"gamenode"
	"gamenode/internal/database"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err := database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestEnsureGeneratesOnce(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := New(db, "dev")
	id1, err := s.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" {
		t.Fatal("expected non-empty node id")
	}
	id2, err := s.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("node id changed across calls: %q vs %q", id1, id2)
	}
}

func TestEnsureSurvivesReload(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	first := New(db, "dev")
	id1, err := first.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh Service instance over the same database simulates a process
	// restart: identity must be read back, not regenerated.
	second := New(db, "dev")
	id2, err := second.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("node id did not survive reload: %q vs %q", id1, id2)
	}
}

func TestEnsureRejectsMalformedPersistedIdentity(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO node_identity(id,node_id,display_name,created_at) VALUES('local','not valid base64 !!','','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s := New(db, "dev")
	if _, err := s.Ensure(ctx); err == nil {
		t.Fatal("expected malformed persisted identity to be rejected")
	}
}

func TestNodeIDIndependentOfDisplayName(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := New(db, "dev")
	id1, err := s.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDisplayName(ctx, "Renamed Host"); err != nil {
		t.Fatal(err)
	}
	id2, err := s.Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatal("node id must not change when display name (metadata only) changes")
	}
}

func TestLocalInfoCapabilities(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := New(db, "1.2.3")
	info, err := s.LocalInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol version = %d", info.ProtocolVersion)
	}
	if info.GameNodeVersion != "1.2.3" {
		t.Fatalf("version = %q", info.GameNodeVersion)
	}
	if len(info.Capabilities) == 0 {
		t.Fatal("expected non-empty capability list")
	}
	for _, c := range info.Capabilities {
		if c == "egg_runtime" {
			t.Fatal("v0.4 egg runtime must not be advertised on this branch")
		}
	}
}

func TestNormalizeDisplayName(t *testing.T) {
	if _, err := NormalizeDisplayName("bad/name"); err == nil {
		t.Fatal("expected error for invalid display name")
	}
	name, err := NormalizeDisplayName("  My Node  ")
	if err != nil || name != "My Node" {
		t.Fatalf("normalize = %q, %v", name, err)
	}
}
