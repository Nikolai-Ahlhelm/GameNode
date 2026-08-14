package settings_test

import (
	"context"
	"testing"

	"gamenode"
	"gamenode/internal/database"
	"gamenode/internal/settings"
)

func newService(t *testing.T) (*settings.Service, func()) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	return settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: 5, MonitoringHistoryLimit: 300}), func() { db.Close() }
}

func TestDefaultsAndPersistence(t *testing.T) {
	s, closeDB := newService(t)
	defer closeDB()
	ctx := context.Background()
	initial, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Monitoring.SampleIntervalSeconds != 5 || initial.Monitoring.HistoryLimit != 300 || !initial.RestartRequired {
		t.Fatalf("unexpected defaults: %+v", initial)
	}
	interval, history := 7, 450
	updated, changed, err := s.Update(ctx, settings.Patch{Monitoring: &settings.MonitoringPatch{SampleIntervalSeconds: &interval, HistoryLimit: &history}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 || updated.Monitoring.SampleIntervalSeconds != interval || updated.Monitoring.HistoryLimit != history {
		t.Fatalf("unexpected update: %+v %#v", updated, changed)
	}
	reloaded, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Monitoring != updated.Monitoring {
		t.Fatalf("persisted values were not reloaded: %+v", reloaded)
	}
}

func TestValidationAndPartialUpdate(t *testing.T) {
	s, closeDB := newService(t)
	defer closeDB()
	ctx := context.Background()
	history := 500
	updated, changed, err := s.Update(ctx, settings.Patch{Monitoring: &settings.MonitoringPatch{HistoryLimit: &history}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "monitoring.history_limit" || updated.Monitoring.SampleIntervalSeconds != 5 {
		t.Fatalf("partial update changed unexpected values: %+v %#v", updated, changed)
	}
	invalid := 0
	if _, _, err := s.Update(ctx, settings.Patch{Monitoring: &settings.MonitoringPatch{SampleIntervalSeconds: &invalid}}); err == nil {
		t.Fatal("expected interval validation failure")
	}
}

func TestLoggingLevelPersistence(t *testing.T) {
	s, closeDB := newService(t)
	defer closeDB()
	level := "debug"
	updated, changed, err := s.Update(context.Background(), settings.Patch{Logging: &settings.LoggingPatch{Level: &level}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Logging.Level != "debug" || len(changed) != 1 || changed[0] != "logging.level" {
		t.Fatalf("unexpected logging update: %+v %#v", updated, changed)
	}
	invalid := "trace"
	if _, _, err := s.Update(context.Background(), settings.Patch{Logging: &settings.LoggingPatch{Level: &invalid}}); err == nil {
		t.Fatal("expected validation failure")
	}
}
