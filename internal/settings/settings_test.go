package settings_test

import (
	"context"
	"reflect"
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

func TestContainerImageAllowlistPersistenceAndValidation(t *testing.T) {
	s, closeDB := newService(t)
	defer closeDB()
	ctx := context.Background()
	allowlist := []string{"registry.example:5000", "GHCR.IO"}
	updated, changed, err := s.Update(ctx, settings.Patch{Runtime: &settings.RuntimePatch{ContainerImageAllowlist: &allowlist}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || updated.Runtime.ContainerImageAllowlist[0] != "registry.example:5000" || updated.Runtime.ContainerImageAllowlist[1] != "ghcr.io" {
		t.Fatalf("unexpected allowlist update: %#v %#v", updated.Runtime, changed)
	}
	reloaded, err := s.Get(ctx)
	if err != nil || !reflect.DeepEqual(reloaded.Runtime.ContainerImageAllowlist, updated.Runtime.ContainerImageAllowlist) {
		t.Fatalf("allowlist was not persisted: %#v %v", reloaded.Runtime, err)
	}
	invalid := []string{"https://registry.example"}
	if _, _, err := s.Update(ctx, settings.Patch{Runtime: &settings.RuntimePatch{ContainerImageAllowlist: &invalid}}); err == nil {
		t.Fatal("invalid registry allowlist accepted")
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

func TestLoggingCategoryDefaultsAllEnabled(t *testing.T) {
	s, closeDB := newService(t)
	defer closeDB()
	values, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := settings.LoggingCategories{HTTP: true, Database: true, Runtime: true, Auth: true, Filesystem: true, Provisioning: true, SteamCMD: true, Templates: true, General: true}
	if values.Logging.Categories != want {
		t.Fatalf("unexpected default categories: %+v", values.Logging.Categories)
	}
	if values.Logging.DetailedErrors {
		t.Fatal("detailed errors must default to disabled")
	}
}

func TestLoggingCategoryPartialUpdateAndPersistence(t *testing.T) {
	s, closeDB := newService(t)
	defer closeDB()
	ctx := context.Background()
	disabled := false
	updated, changed, err := s.Update(ctx, settings.Patch{Logging: &settings.LoggingPatch{Categories: &settings.LoggingCategoriesPatch{HTTP: &disabled}}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Logging.Categories.HTTP || !updated.Logging.Categories.Database {
		t.Fatalf("unexpected category state after partial update: %+v", updated.Logging.Categories)
	}
	if len(changed) != 1 || changed[0] != "logging.category.http" {
		t.Fatalf("unexpected changed fields: %#v", changed)
	}
	reloaded, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Logging.Categories != updated.Logging.Categories {
		t.Fatalf("persisted category state was not reloaded: %+v vs %+v", reloaded.Logging.Categories, updated.Logging.Categories)
	}
}

func TestDetailedErrorsSettingPersists(t *testing.T) {
	s, closeDB := newService(t)
	defer closeDB()
	ctx := context.Background()
	enabled := true
	updated, changed, err := s.Update(ctx, settings.Patch{Logging: &settings.LoggingPatch{DetailedErrors: &enabled}})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Logging.DetailedErrors || len(changed) != 1 || changed[0] != "logging.detailed_errors" {
		t.Fatalf("unexpected detailed-errors update: %+v %#v", updated, changed)
	}
	reloaded, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Logging.DetailedErrors {
		t.Fatal("persisted detailed-errors flag was not reloaded")
	}
}

// Settings stays a strict whitelist: unknown fields inside a JSON patch body
// - including inside the nested logging.categories object - are rejected by
// the request decoder's DisallowUnknownFields before ever reaching this
// package, so there is no way to set arbitrary logger configuration through
// the API. See internal/api/settings_test.go for the end-to-end check.
func TestLoggingCategoriesPatchHasNoArbitraryFields(t *testing.T) {
	fields := reflect.TypeOf(settings.LoggingCategoriesPatch{})
	for i := 0; i < fields.NumField(); i++ {
		name := fields.Field(i).Name
		found := false
		for _, known := range []string{"HTTP", "Database", "Runtime", "Auth", "Filesystem", "Provisioning", "SteamCMD", "Templates", "General"} {
			if name == known {
				found = true
			}
		}
		if !found {
			t.Fatalf("unexpected field on the logging categories whitelist: %s", name)
		}
	}
}
