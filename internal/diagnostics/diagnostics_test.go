package diagnostics_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/database"
	"gamenode/internal/diagnostics"
	"gamenode/internal/settings"
)

func TestSnapshotIsSafeAndPopulated(t *testing.T) {
	t.Setenv("GAMENODE_DIAGNOSTIC_SECRET", "SHOULD_NEVER_APPEAR")
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	s := diagnostics.New(db, settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: 5, MonitoringHistoryLimit: 300}), diagnostics.MonitoringEffective{SampleIntervalSeconds: 5, HistoryLimit: 300}, time.Now().UTC())
	result := s.Get(context.Background())
	if result.Platform.OS == "" || result.Platform.Arch == "" || result.Platform.LogicalCPUs < 1 || !result.Database.Healthy || result.Database.SchemaVersion == "" {
		t.Fatalf("incomplete diagnostics: %+v", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), os.Getenv("GAMENODE_DIAGNOSTIC_SECRET")) {
		t.Fatal("diagnostics leaked environment value")
	}
}
