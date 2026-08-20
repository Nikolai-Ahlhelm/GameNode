package statushistory_test

import (
	"context"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/database"
	"gamenode/internal/statushistory"
)

func testStore(t *testing.T) *statushistory.Store {
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
	// The foreign key is intentional, so create a server row through the
	// schema-independent minimum columns needed by the history table.
	if _, err := db.Exec(`INSERT INTO servers(id,tenant_id,creation_mode,name,description,working_directory,executable,arguments_json,environment_json,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,auto_restart_enabled,auto_restart_max_attempts,auto_restart_window_seconds,auto_restart_delay_seconds,runtime_type,created_at,updated_at) VALUES('00000000-0000-4000-a000-000000000001','default','custom','History test','','C:\\servers\\history','game.exe','[]','{}',0,'never','terminate','',30,0,0,0,0,'native','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return statushistory.New(db)
}

func TestRecordListAndPrune(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	if err := store.RecordBatch(ctx, []statushistory.Check{{ServerID: "00000000-0000-4000-a000-000000000001", CheckedAt: now.Add(-31 * 24 * time.Hour), Status: "up", State: "running"}, {ServerID: "00000000-0000-4000-a000-000000000001", CheckedAt: now, Status: "down", State: "stopped"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneBefore(ctx, now.Add(-statushistory.Retention)); err != nil {
		t.Fatal(err)
	}
	checks, err := store.List(ctx, "00000000-0000-4000-a000-000000000001", now.Add(-statushistory.Retention))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Status != "down" || checks[0].State != "stopped" {
		t.Fatalf("unexpected checks: %#v", checks)
	}
}
