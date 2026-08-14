package support_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/audit"
	"gamenode/internal/database"
	"gamenode/internal/diagnostics"
	"gamenode/internal/monitoring"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/settings"
	"gamenode/internal/support"
)

type failingAudit struct{ err error }

func (f failingAudit) List(context.Context, audit.Filter) ([]audit.Event, error) { return nil, f.err }

type staticSettings struct {
	values settings.Values
	err    error
}

func (s staticSettings) Get(context.Context) (settings.Values, error) { return s.values, s.err }

type failingServers struct{ err error }

func (f failingServers) List(context.Context) ([]servers.Record, error) { return nil, f.err }
func (f failingServers) MonitoringSnapshot(context.Context, string) (monitoring.Snapshot, error) {
	return monitoring.Snapshot{}, f.err
}

func testBundle(t *testing.T) []byte {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	st := settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: 5, MonitoringHistoryLimit: 300})
	diag := diagnostics.New(db, st, diagnostics.MonitoringEffective{SampleIntervalSeconds: 5, HistoryLimit: 300}, time.Now().UTC())
	srv := servers.NewService(servers.NewStore(db), runtime.NewNative())
	service := support.New(diag, st, audit.New(db), srv)
	var out bytes.Buffer
	if err := service.Generate(context.Background(), &out, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func testDependencies(t *testing.T) (*diagnostics.Service, *settings.Service, *audit.Service, *servers.Service) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	st := settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: 5, MonitoringHistoryLimit: 300})
	return diagnostics.New(db, st, diagnostics.MonitoringEffective{SampleIntervalSeconds: 5, HistoryLimit: 300}, time.Now().UTC()), st, audit.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative())
}

func bundleEntry(t *testing.T, bundle []byte, name string) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		data, err := io.ReadAll(stream)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	t.Fatalf("%s missing", name)
	return nil
}

func recordAudit(t *testing.T, service *audit.Service, timestamp time.Time, id string) {
	t.Helper()
	if err := service.Record(context.Background(), audit.Event{ID: id, Timestamp: timestamp, Action: audit.SettingsUpdate, ResourceType: audit.Settings, Result: audit.Success, ResourceName: id}); err != nil {
		t.Fatal(err)
	}
}

func incompressibleASCII(n int) string {
	data := make([]byte, n)
	state := uint32(1)
	for i := range data {
		state = state*1664525 + 1013904223
		data[i] = byte(33 + state%92)
		if data[i] == '"' || data[i] == '\\' {
			data[i] = 'x'
		}
	}
	return string(data)
}

func TestBundleStructureAndManifest(t *testing.T) {
	bundle := testBundle(t)
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"audit-recent.json", "diagnostics.json", "manifest.json", "servers.json", "settings.json"}
	got := make([]string, 0, len(reader.File))
	seen := map[string]bool{}
	var manifest struct {
		BundleSchemaVersion int       `json:"bundle_schema_version"`
		GeneratedAt         time.Time `json:"generated_at"`
		Format              string    `json:"format"`
		Warnings            []string  `json:"warnings"`
	}
	for _, file := range reader.File {
		got = append(got, file.Name)
		if seen[file.Name] || strings.Contains(file.Name, "..") || strings.HasPrefix(file.Name, "/") || strings.HasPrefix(file.Name, "\\") || strings.Contains(file.Name, "\\") || (len(file.Name) > 1 && file.Name[1] == ':') {
			t.Fatalf("unsafe or duplicate entry %q", file.Name)
		}
		seen[file.Name] = true
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		_, err = data.ReadFrom(stream)
		stream.Close()
		if err != nil || !json.Valid(data.Bytes()) {
			t.Fatalf("invalid JSON in %s", file.Name)
		}
		if file.Name == "manifest.json" {
			if err := json.Unmarshal(data.Bytes(), &manifest); err != nil {
				t.Fatal(err)
			}
		}
	}
	sort.Strings(got)
	if len(got) != len(want) || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	if manifest.BundleSchemaVersion != 1 || manifest.Format != "zip" || manifest.GeneratedAt.IsZero() || manifest.GeneratedAt.Location() != time.UTC || len(manifest.Warnings) != 0 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
}

func TestSettingsExportIsWhitelisted(t *testing.T) {
	bundle := testBundle(t)
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != "settings.json" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		_, err = data.ReadFrom(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			Monitoring struct {
				SampleIntervalSeconds int `json:"sample_interval_seconds"`
				HistoryLimit          int `json:"history_limit"`
			} `json:"monitoring"`
			Security struct {
				PasswordMinimumLength int `json:"password_minimum_length"`
				PasswordMaximumLength int `json:"password_maximum_length"`
			} `json:"security"`
			Branding struct {
				Name          string `json:"name"`
				Subtitle      string `json:"subtitle"`
				CustomFavicon bool   `json:"custom_favicon"`
			} `json:"branding"`
			RestartRequired       bool     `json:"restart_required"`
			RestartRequiredFields []string `json:"restart_required_fields"`
		}
		if err := json.Unmarshal(data.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if value.Monitoring.SampleIntervalSeconds != 5 || value.Monitoring.HistoryLimit != 300 || value.Security.PasswordMinimumLength != 8 || value.Security.PasswordMaximumLength != 256 || value.Branding.Name != "GameNode" || value.Branding.Subtitle != "Infrastructure manager" || !value.RestartRequired {
			t.Fatalf("unexpected settings export: %+v", value)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 6 || raw["monitoring"] == nil || raw["logging"] == nil || raw["security"] == nil || raw["branding"] == nil || raw["restart_required"] == nil || raw["restart_required_fields"] == nil {
			t.Fatalf("unexpected settings fields: %v", raw)
		}
		return
	}
	t.Fatal("settings.json missing")
}

func TestUnknownAppSettingIsExcluded(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	const secret = "SUPPORT_UNKNOWN_SETTING_SECRET_SHOULD_NEVER_APPEAR"
	if _, err = db.Exec(`INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,?)`, "support.test.secret_setting", secret, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	st := settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: 5, MonitoringHistoryLimit: 300})
	diag := diagnostics.New(db, st, diagnostics.MonitoringEffective{SampleIntervalSeconds: 5, HistoryLimit: 300}, time.Now().UTC())
	srv := servers.NewService(servers.NewStore(db), runtime.NewNative())
	var out bytes.Buffer
	if err = support.New(diag, st, audit.New(db), srv).Generate(context.Background(), &out, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("unknown app setting leaked into bundle")
	}
	reader, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != "settings.json" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		_, err = data.ReadFrom(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 6 || raw["monitoring"] == nil || raw["logging"] == nil || raw["security"] == nil || raw["branding"] == nil || raw["restart_required"] == nil || raw["restart_required_fields"] == nil || strings.Contains(data.String(), secret) {
			t.Fatal("unknown setting leaked into settings export")
		}
		return
	}
	t.Fatal("settings.json missing")
}

func TestDiagnosticsExportIsSanitized(t *testing.T) {
	t.Setenv("GAMENODE_SUPPORT_DIAGNOSTICS_SECRET", "SUPPORT_DIAGNOSTICS_ENV_SECRET_SHOULD_NEVER_APPEAR")
	bundle := testBundle(t)
	if strings.Contains(string(bundle), "SUPPORT_DIAGNOSTICS_ENV_SECRET_SHOULD_NEVER_APPEAR") {
		t.Fatal("environment leaked into bundle")
	}
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != "diagnostics.json" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		_, err = data.ReadFrom(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		var value struct {
			Status      string `json:"status"`
			Application struct {
				GoVersion        string    `json:"go_version"`
				UptimeSeconds    int64     `json:"uptime_seconds"`
				ProcessStartedAt time.Time `json:"process_started_at"`
			} `json:"application"`
			Platform struct {
				OS          string `json:"os"`
				Arch        string `json:"arch"`
				LogicalCPUs int    `json:"logical_cpus"`
			} `json:"platform"`
			Database struct {
				Type          string `json:"type"`
				SchemaVersion string `json:"schema_version"`
				Healthy       bool   `json:"healthy"`
			} `json:"database"`
			Monitoring struct {
				SampleIntervalSeconds int  `json:"sample_interval_seconds"`
				HistoryLimit          int  `json:"history_limit"`
				RestartRequired       bool `json:"restart_required"`
			} `json:"monitoring"`
		}
		if err := json.Unmarshal(data.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if value.Status == "" || value.Application.GoVersion == "" || value.Application.UptimeSeconds < 0 || value.Application.ProcessStartedAt.IsZero() || value.Platform.OS == "" || value.Platform.Arch == "" || value.Platform.LogicalCPUs < 1 || value.Database.Type != "sqlite" || value.Database.SchemaVersion == "" || !value.Database.Healthy || value.Monitoring.SampleIntervalSeconds != 5 || value.Monitoring.HistoryLimit != 300 || !value.Monitoring.RestartRequired {
			t.Fatalf("unexpected diagnostics: %+v", value)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 5 || raw["status"] == nil || raw["application"] == nil || raw["platform"] == nil || raw["database"] == nil || raw["monitoring"] == nil || strings.Contains(data.String(), "SUPPORT_DIAGNOSTICS_ENV_SECRET_SHOULD_NEVER_APPEAR") {
			t.Fatal("diagnostics whitelist/sanitization failed")
		}
		return
	}
	t.Fatal("diagnostics.json missing")
}

func TestServerSummaryExportIsSanitized(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	st := settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: 5, MonitoringHistoryLimit: 300})
	diag := diagnostics.New(db, st, diagnostics.MonitoringEffective{SampleIntervalSeconds: 5, HistoryLimit: 300}, time.Now().UTC())
	srv := servers.NewService(servers.NewStore(db), runtime.NewNative())
	workingDirectory := filepath.Join(t.TempDir(), "SUPPORT_HOST_PATH_SECRET_SHOULD_NEVER_APPEAR")
	if err = os.Mkdir(workingDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record, err := srv.Create(context.Background(), servers.Server{CreationMode: servers.CreationCustom, Name: "Support Sanitization Test", WorkingDirectory: workingDirectory, Executable: executable, Arguments: []string{"SUPPORT_ARG_SECRET_SHOULD_NEVER_APPEAR"}, EnvironmentVariables: map[string]string{"SECRET": "SUPPORT_ENV_SECRET_SHOULD_NEVER_APPEAR"}, RuntimeType: "native", StopMethod: "terminate", StopTimeoutSeconds: 15, AutoRestartEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE server_runtime_state SET current_state=?,crash_count=?,restart_count=? WHERE server_id=?`, servers.StateStopped, 3, 4, record.Server.ID); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = support.New(diag, st, audit.New(db), srv).Generate(context.Background(), &out, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SUPPORT_ARG_SECRET_SHOULD_NEVER_APPEAR", "SUPPORT_ENV_SECRET_SHOULD_NEVER_APPEAR", "SUPPORT_HOST_PATH_SECRET_SHOULD_NEVER_APPEAR"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("server secret leaked: %s", secret)
		}
	}
	reader, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name != "servers.json" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		_, err = data.ReadFrom(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		var items []map[string]any
		if err = json.Unmarshal(data.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item["id"] != record.Server.ID {
				continue
			}
			want := map[string]bool{"id": true, "name": true, "state": true, "health": true, "auto_restart_enabled": true, "crash_count": true, "restart_count": true}
			if len(item) != len(want) {
				t.Fatalf("unexpected summary fields: %v", item)
			}
			for key := range item {
				if !want[key] {
					t.Fatalf("unexpected summary key %s", key)
				}
			}
			if item["name"] != "Support Sanitization Test" || item["state"] != servers.StateStopped || item["health"] != "stopped" || item["auto_restart_enabled"] != true || item["crash_count"] != float64(3) || item["restart_count"] != float64(4) {
				t.Fatalf("unexpected summary: %v", item)
			}
			return
		}
		t.Fatal("test server missing")
	}
	t.Fatal("servers.json missing")
}

func TestSupportBundleExcludesInjectedSentinels(t *testing.T) {
	t.Setenv("GAMENODE_SUPPORT_TEST_SECRET", "SUPPORT_DIAGNOSTICS_ENV_SECRET_SHOULD_NEVER_APPEAR")
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	const unknown = "SUPPORT_UNKNOWN_SETTING_SECRET_SHOULD_NEVER_APPEAR"
	if _, err = db.Exec(`INSERT INTO app_settings(key,value,updated_at) VALUES(?,?,?)`, `support.test.secret_setting`, unknown, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	st := settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: 5, MonitoringHistoryLimit: 300})
	diag := diagnostics.New(db, st, diagnostics.MonitoringEffective{SampleIntervalSeconds: 5, HistoryLimit: 300}, time.Now().UTC())
	srv := servers.NewService(servers.NewStore(db), runtime.NewNative())
	workingDirectory := filepath.Join(t.TempDir(), "SUPPORT_HOST_PATH_SECRET_SHOULD_NEVER_APPEAR")
	if err = os.Mkdir(workingDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.Create(context.Background(), servers.Server{CreationMode: servers.CreationCustom, Name: "Support Sentinel Test", WorkingDirectory: workingDirectory, Executable: executable, Arguments: []string{"SUPPORT_ARG_SECRET_SHOULD_NEVER_APPEAR"}, EnvironmentVariables: map[string]string{"SECRET": "SUPPORT_ENV_SECRET_SHOULD_NEVER_APPEAR"}, RuntimeType: "native", StopMethod: "terminate", StopTimeoutSeconds: 15})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = support.New(diag, st, audit.New(db), srv).Generate(context.Background(), &out, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SUPPORT_DIAGNOSTICS_ENV_SECRET_SHOULD_NEVER_APPEAR", unknown, "SUPPORT_ARG_SECRET_SHOULD_NEVER_APPEAR", "SUPPORT_ENV_SECRET_SHOULD_NEVER_APPEAR", "SUPPORT_HOST_PATH_SECRET_SHOULD_NEVER_APPEAR"} {
		if bytes.Contains(out.Bytes(), []byte(secret)) {
			t.Fatalf("bundle leaked %s", secret)
		}
	}
}

func TestSupportBundleHasNoSensitiveSubsystemExports(t *testing.T) {
	bundle := testBundle(t)
	reader, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"manifest.json": true, "diagnostics.json": true, "settings.json": true, "audit-recent.json": true, "servers.json": true}
	forbidden := map[string]bool{"password": true, "passwords": true, "password_hash": true, "session": true, "sessions": true, "token": true, "tokens": true, "csrf": true, "cookie": true, "cookies": true, "files": true, "uploads": true, "console": true, "stdin": true, "stdout": true, "stderr": true}
	if len(reader.File) != len(want) {
		t.Fatalf("unexpected entry count %d", len(reader.File))
	}
	for _, file := range reader.File {
		if !want[file.Name] {
			t.Fatalf("unexpected export %s", file.Name)
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		_, err = data.ReadFrom(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		var raw any
		if err = json.Unmarshal(data.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if object, ok := raw.(map[string]any); ok {
			for key := range object {
				if forbidden[strings.ToLower(key)] {
					t.Fatalf("sensitive top-level export %s in %s", key, file.Name)
				}
			}
		}
	}
}

func TestAuditSnapshotIsLimitedToOneHundredEvents(t *testing.T) {
	diag, st, audits, srv := testDependencies(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < support.AuditLimit+25; i++ {
		recordAudit(t, audits, start.Add(time.Duration(i)*time.Second), fmt.Sprintf("event-%03d", i))
	}
	var out bytes.Buffer
	if err := support.New(diag, st, audits, srv).Generate(context.Background(), &out, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	var events []audit.Event
	if err := json.Unmarshal(bundleEntry(t, out.Bytes(), "audit-recent.json"), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != support.AuditLimit {
		t.Fatalf("exported %d events, want %d", len(events), support.AuditLimit)
	}
}

func TestAuditSnapshotIsNewestFirst(t *testing.T) {
	diag, st, audits, srv := testDependencies(t)
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		recordAudit(t, audits, start.Add(time.Duration(i)*time.Second), fmt.Sprintf("ordered-event-%d", i))
	}
	want, err := audits.List(context.Background(), audit.Filter{Limit: support.AuditLimit})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := support.New(diag, st, audits, srv).Generate(context.Background(), &out, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	var got []audit.Event
	if err := json.Unmarshal(bundleEntry(t, out.Bytes(), "audit-recent.json"), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("exported %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("event %d = %q, want newest-first %q", i, got[i].ID, want[i].ID)
		}
	}
}

func TestAuditReadFailureProducesEmptySnapshotAndWarning(t *testing.T) {
	diag, st, _, srv := testDependencies(t)
	var out bytes.Buffer
	if err := support.New(diag, st, failingAudit{errors.New("audit read failed")}, srv).Generate(context.Background(), &out, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	var events []audit.Event
	if err := json.Unmarshal(bundleEntry(t, out.Bytes(), "audit-recent.json"), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("audit events = %d, want empty", len(events))
	}
	var manifest struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(bundleEntry(t, out.Bytes(), "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Warnings) != 1 || manifest.Warnings[0] != "recent audit events unavailable" {
		t.Fatalf("warnings = %v", manifest.Warnings)
	}
}

func TestAuditReadFailureDoesNotLeakRawError(t *testing.T) {
	diag, st, _, srv := testDependencies(t)
	const secret = "SUPPORT_RAW_ERROR_SECRET_SHOULD_NEVER_APPEAR"
	var out bytes.Buffer
	if err := support.New(diag, st, failingAudit{fmt.Errorf("database failure: %s", secret)}, srv).Generate(context.Background(), &out, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte(secret)) {
		t.Fatal("raw audit error leaked into bundle")
	}
}

func TestEssentialSourceFailuresReturnError(t *testing.T) {
	diag, st, audits, srv := testDependencies(t)
	for name, service := range map[string]*support.Service{
		"settings": support.New(diag, staticSettings{err: errors.New("settings unavailable")}, audits, srv),
		"servers":  support.New(diag, st, audits, failingServers{errors.New("servers unavailable")}),
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := service.Generate(context.Background(), &out, support.Scope{}); err == nil {
				t.Fatal("Generate succeeded despite essential source failure")
			}
		})
	}
}

func TestBundleSizeGuard(t *testing.T) {
	diag, st, audits, srv := testDependencies(t)
	t.Run("normal bundle succeeds", func(t *testing.T) {
		var out bytes.Buffer
		if err := support.New(diag, st, audits, srv).Generate(context.Background(), &out, support.Scope{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("oversized bundle returns sentinel", func(t *testing.T) {
		// A single incompressible whitelisted value exercises the ZIP output guard.
		large := []string{incompressibleASCII(int(support.MaxBundleBytes * 13 / 10))}
		var out bytes.Buffer
		err := support.New(diag, staticSettings{values: settings.Values{RestartRequiredFields: large}}, audits, srv).Generate(context.Background(), &out, support.Scope{})
		if !errors.Is(err, support.ErrBundleTooLarge) {
			t.Fatalf("Generate error = %v, want ErrBundleTooLarge", err)
		}
	})
}
