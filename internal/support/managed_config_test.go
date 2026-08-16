package support_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/audit"
	"gamenode/internal/database"
	"gamenode/internal/diagnostics"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/settings"
	"gamenode/internal/support"
	"gamenode/internal/templates"
)

// TestSupportBundleAndDiagnosticsExcludeManagedLaunchSecrets proves the new
// managed configuration store is not reachable through the whitelisted
// diagnostic exports.
func TestSupportBundleAndDiagnosticsExcludeManagedLaunchSecrets(t *testing.T) {
	const secret = "MANAGED_LAUNCH_SECRET_SHOULD_NEVER_APPEAR"
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "game.exe"), []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	maxLength := 64
	definition := templates.ConfigAdapterDefinition{
		SchemaVersion:   templates.AdapterSchemaVersion,
		ID:              "valheim-settings",
		Version:         "1.0.0",
		Format:          templates.FormatManagedLaunch,
		RestartRequired: true,
		Fields: []templates.ConfigAdapterField{
			{Key: "SERVER_PASSWORD", Label: "Server password", Type: "secret", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchSecret, Argument: "-password"}},
			{Key: "API_TOKEN", Label: "API token", Type: "secret", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingEnvironmentSecret, Name: "GAME_TOKEN"}},
		},
	}
	raw, _ := json.Marshal(definition)
	serverService := servers.NewService(servers.NewStore(db), runtime.NewNative())
	if _, err = serverService.CreateProvisioned(context.Background(), servers.Server{Name: "Managed Secret Server", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate}, "valheim", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: definition.SchemaVersion, Version: definition.Version, TemplateID: "valheim", TemplateVersion: "1.1.0", DefinitionJSON: raw, Values: []servers.ProvisionedConfigValue{{Key: "SERVER_PASSWORD", Value: secret, Sensitive: true}, {Key: "API_TOKEN", Value: secret, Sensitive: true}}}}); err != nil {
		t.Fatal(err)
	}
	// The value really is stored, so the assertions below are meaningful.
	var stored string
	if err = db.QueryRow(`SELECT value FROM server_config_values WHERE field_key='SERVER_PASSWORD'`).Scan(&stored); err != nil || stored != secret {
		t.Fatalf("managed value was not persisted: %q %v", stored, err)
	}

	settingService := settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: 5, MonitoringHistoryLimit: 300})
	diagnosticService := diagnostics.New(db, settingService, diagnostics.MonitoringEffective{SampleIntervalSeconds: 5, HistoryLimit: 300}, time.Now().UTC())
	encoded, err := json.Marshal(diagnosticService.Get(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("diagnostics leaked a managed secret: %s", encoded)
	}
	var bundle bytes.Buffer
	if err = support.New(diagnosticService, settingService, audit.New(db), serverService).Generate(context.Background(), &bundle, support.Scope{}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bundle.Bytes(), []byte(secret)) {
		t.Fatal("support bundle leaked a managed secret")
	}
}
