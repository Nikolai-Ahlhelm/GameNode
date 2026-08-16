package gameconfig_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode"
	"gamenode/internal/database"
	"gamenode/internal/gameconfig"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

const launchSecretValue = "LAUNCH_SECRET_SHOULD_NEVER_APPEAR"

func launchDefinition() templates.ConfigAdapterDefinition {
	maxLength := 64
	minimum, maximum := float64(60), float64(86400)
	return templates.ConfigAdapterDefinition{
		SchemaVersion:   templates.AdapterSchemaVersion,
		ID:              "valheim-settings",
		Version:         "1.0.0",
		Format:          templates.FormatManagedLaunch,
		RestartRequired: true,
		Fields: []templates.ConfigAdapterField{
			{Key: "SERVER_NAME", Label: "Server name", Section: "General", Type: "string", Required: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchValue, Argument: "-name"}},
			{Key: "SERVER_PASSWORD", Label: "Password", Section: "Security", Type: "secret", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchSecret, Argument: "-password"}},
			{Key: "PUBLIC", Label: "Public", Section: "Networking", Type: "boolean", Required: true, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchValue, Argument: "-public", TrueValue: "1", FalseValue: "0"}},
			{Key: "CROSSPLAY", Label: "Crossplay", Section: "Networking", Type: "boolean", Required: true, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchFlag, Argument: "-crossplay"}},
			{Key: "SAVE_INTERVAL", Label: "Save interval", Section: "Gameplay", Type: "integer", Required: true, Validation: templates.Validation{Min: &minimum, Max: &maximum}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchValue, Argument: "-saveinterval"}},
			{Key: "REGION", Label: "Region", Section: "Gameplay", Type: "string", Nullable: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingEnvironmentValue, Name: "GAME_REGION"}},
			{Key: "API_TOKEN", Label: "API token", Section: "Security", Type: "secret", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingEnvironmentSecret, Name: "GAME_TOKEN"}},
		},
	}
}

// newLaunchService builds a real server with the managed-launch adapter and the
// supplied initial values, exercising the same transactional path provisioning
// uses.
func newLaunchService(t *testing.T, values []servers.ProvisionedConfigValue) (*gameconfig.Service, *servers.Service, string, func()) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "game.exe"), []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	definition := launchDefinition()
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	record, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "Valheim", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate, Arguments: []string{"-nographics", "-batchmode"}}, "valheim", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: definition.SchemaVersion, Version: definition.Version, TemplateID: "valheim", TemplateVersion: "1.1.0", DefinitionJSON: raw, Values: values}})
	if err != nil {
		t.Fatal(err)
	}
	return gameconfig.New(db, serverService), serverService, record.Server.ID, func() { db.Close() }
}

func fullValues() []servers.ProvisionedConfigValue {
	return []servers.ProvisionedConfigValue{
		{Key: "SERVER_NAME", Value: "My Valheim"},
		{Key: "SERVER_PASSWORD", Value: launchSecretValue, Sensitive: true},
		{Key: "PUBLIC", Value: "0"},
		{Key: "CROSSPLAY", Value: "1"},
		{Key: "SAVE_INTERVAL", Value: "1800"},
		{Key: "REGION", Value: "eu"},
		{Key: "API_TOKEN", Value: launchSecretValue, Sensitive: true},
	}
}

// TestResolveLaunchProducesExactArgv proves every binding type and the central
// invariant: one user value is exactly one argv element.
func TestResolveLaunchProducesExactArgv(t *testing.T) {
	service, _, serverID, cleanup := newLaunchService(t, fullValues())
	defer cleanup()
	arguments, environment, err := service.ResolveLaunch(context.Background(), serverID, []string{"-nographics", "-batchmode"}, map[string]string{"STEAMAPPID": "892970"})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"-nographics", "-batchmode", "-name", "My Valheim", "-password", launchSecretValue, "-public", "0", "-crossplay", "-saveinterval", "1800"}
	if len(arguments) != len(expected) {
		t.Fatalf("argument count %d, expected %d: %#v", len(arguments), len(expected), arguments)
	}
	for index, value := range expected {
		if arguments[index] != value {
			t.Fatalf("argument %d is %q, expected %q (%#v)", index, arguments[index], value, arguments)
		}
	}
	if environment["GAME_REGION"] != "eu" || environment["GAME_TOKEN"] != launchSecretValue || environment["STEAMAPPID"] != "892970" {
		t.Fatalf("unexpected environment: %#v", environment)
	}
}

// TestResolveLaunchValueStaysOneArgument proves a user value containing spaces
// and shell metacharacters cannot become additional argv elements.
func TestResolveLaunchValueStaysOneArgument(t *testing.T) {
	hostile := `My Server" -public 1 && rm -rf / ; echo $(id)`
	values := fullValues()
	values[0].Value = hostile
	service, _, serverID, cleanup := newLaunchService(t, values)
	defer cleanup()
	arguments, _, err := service.ResolveLaunch(context.Background(), serverID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	index := -1
	for position, value := range arguments {
		if value == "-name" {
			index = position
		}
	}
	if index < 0 || arguments[index+1] != hostile {
		t.Fatalf("hostile value was not preserved as one element: %#v", arguments)
	}
	// Exactly one -public argument may exist; the hostile value must not add one.
	count := 0
	for _, value := range arguments {
		if value == "-public" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("hostile value injected extra arguments: %#v", arguments)
	}
}

func TestResolveLaunchFlagAndBooleanMapping(t *testing.T) {
	values := fullValues()
	values[2].Value = "1" // PUBLIC enabled
	values[3].Value = "0" // CROSSPLAY disabled
	service, _, serverID, cleanup := newLaunchService(t, values)
	defer cleanup()
	arguments, _, err := service.ResolveLaunch(context.Background(), serverID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, " ")
	if !strings.Contains(joined, "-public 1") {
		t.Fatalf("boolean mapping missing: %#v", arguments)
	}
	for _, value := range arguments {
		if value == "-crossplay" {
			t.Fatalf("disabled flag must emit no argument: %#v", arguments)
		}
	}
}

// TestResolveLaunchRequiresConfiguredRequiredField fails closed rather than
// starting a differently configured game server.
func TestResolveLaunchRequiresConfiguredRequiredField(t *testing.T) {
	values := fullValues()[1:] // drop the required SERVER_NAME
	service, _, serverID, cleanup := newLaunchService(t, values)
	defer cleanup()
	_, _, err := service.ResolveLaunch(context.Background(), serverID, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "SERVER_NAME") {
		t.Fatalf("expected an incomplete-configuration error, got %v", err)
	}
	if strings.Contains(err.Error(), launchSecretValue) {
		t.Fatal("error leaked a secret")
	}
}

// TestConfigurationGetNeverReturnsSecrets covers the API-facing view model.
func TestConfigurationGetNeverReturnsSecrets(t *testing.T) {
	service, _, serverID, cleanup := newLaunchService(t, fullValues())
	defer cleanup()
	result, err := service.Get(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), launchSecretValue) {
		t.Fatalf("configuration response leaked a secret: %s", body)
	}
	if !result.Available || len(result.Adapters) != 1 || !result.Adapters[0].Ready || !result.Adapters[0].RestartRequired {
		t.Fatalf("unexpected result: %#v", result)
	}
	for _, field := range result.Adapters[0].Fields {
		if field.Sensitive && (field.Value != "" || !field.Configured) {
			t.Fatalf("secret field must be reported as configured without a value: %#v", field)
		}
		if field.Key == "SERVER_NAME" && field.Value != "My Valheim" {
			t.Fatalf("plain field missing: %#v", field)
		}
	}
}

// TestConfigurationUpdateKeepsUnsentSecret protects a configured secret from
// being cleared by an update that does not mention it.
func TestConfigurationUpdateKeepsUnsentSecret(t *testing.T) {
	service, _, serverID, cleanup := newLaunchService(t, fullValues())
	defer cleanup()
	if _, err := service.Update(context.Background(), serverID, "valheim-settings", map[string]string{"SERVER_NAME": "Renamed"}); err != nil {
		t.Fatal(err)
	}
	arguments, _, err := service.ResolveLaunch(context.Background(), serverID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, " ")
	if !strings.Contains(joined, "Renamed") || !strings.Contains(joined, launchSecretValue) {
		t.Fatalf("update dropped the unsent secret: %#v", arguments)
	}
}

func TestConfigurationUpdateRejectsInvalidValues(t *testing.T) {
	service, _, serverID, cleanup := newLaunchService(t, fullValues())
	defer cleanup()
	cases := map[string]map[string]string{
		"unknown field":    {"NOT_A_FIELD": "x"},
		"invalid integer":  {"SAVE_INTERVAL": "abc"},
		"below minimum":    {"SAVE_INTERVAL": "1"},
		"invalid boolean":  {"PUBLIC": "maybe"},
		"newline injected": {"SERVER_NAME": "safe\n-public 1"},
		"nul injected":     {"SERVER_NAME": "safe\x00evil"},
	}
	for name, values := range cases {
		if _, err := service.Update(context.Background(), serverID, "valheim-settings", values); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// TestManagedValuesSurviveRestart reopens the service against the same database
// state to prove persistence rather than in-memory caching.
func TestManagedValuesSurviveRestart(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "gamenode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "game.exe"), []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	definition := launchDefinition()
	raw, _ := json.Marshal(definition)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	record, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "Persisted", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate}, "valheim", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: definition.SchemaVersion, Version: definition.Version, TemplateID: "valheim", TemplateVersion: "1.1.0", DefinitionJSON: raw, Values: fullValues()}})
	if err != nil {
		t.Fatal(err)
	}
	// A second service instance stands in for an application restart.
	restarted := gameconfig.New(db, servers.NewService(servers.NewStore(db), gameRuntime.NewNative()))
	arguments, environment, err := restarted.ResolveLaunch(context.Background(), record.Server.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(arguments, " "), launchSecretValue) || environment["GAME_TOKEN"] != launchSecretValue {
		t.Fatalf("values did not survive restart: %#v %#v", arguments, environment)
	}
	result, err := restarted.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range result.Adapters[0].Fields {
		if field.Key == "SERVER_PASSWORD" && (!field.Sensitive || !field.Configured || field.Value != "") {
			t.Fatalf("secret sensitivity did not survive restart: %#v", field)
		}
	}
}

// TestExistingServerKeepsAdapterSnapshot provisions with adapter 1.0.0, then
// provisions a second server with 1.1.0 from the same database. The existing
// server must keep its pinned snapshot while the new one uses the new version.
func TestExistingServerKeepsAdapterSnapshot(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	service := gameconfig.New(db, serverService)
	create := func(name string, definition templates.ConfigAdapterDefinition) string {
		root := t.TempDir()
		if err = os.WriteFile(filepath.Join(root, "game.exe"), []byte("test"), 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(definition)
		record, createErr := serverService.CreateProvisioned(context.Background(), servers.Server{Name: name, WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate}, "valheim", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: definition.SchemaVersion, Version: definition.Version, TemplateID: "valheim", TemplateVersion: "1.1.0", DefinitionJSON: raw, Values: fullValues()}})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return record.Server.ID
	}
	oldServer := create("Pinned", launchDefinition())
	// The catalog publishes a newer adapter that renames the argument.
	newDefinition := launchDefinition()
	newDefinition.Version = "1.1.0"
	newDefinition.Fields[0].Binding = &templates.ConfigAdapterBinding{Type: templates.BindingLaunchValue, Argument: "-servername"}
	newServer := create("Fresh", newDefinition)

	oldArguments, _, err := service.ResolveLaunch(context.Background(), oldServer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	newArguments, _, err := service.ResolveLaunch(context.Background(), newServer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(oldArguments, " "), "-name") || strings.Contains(strings.Join(oldArguments, " "), "-servername") {
		t.Fatalf("existing server was mutated by the newer adapter: %#v", oldArguments)
	}
	if !strings.Contains(strings.Join(newArguments, " "), "-servername") {
		t.Fatalf("new server did not use the newer adapter: %#v", newArguments)
	}
	existing, err := service.Get(context.Background(), oldServer)
	if err != nil {
		t.Fatal(err)
	}
	if existing.Adapters[0].Version != "1.0.0" {
		t.Fatalf("existing server adapter version changed: %s", existing.Adapters[0].Version)
	}
}

// TestFileAdapterIsNotTreatedAsLaunch keeps the existing file code path intact.
func TestFileAdapterIsNotTreatedAsLaunch(t *testing.T) {
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
	if err = os.WriteFile(filepath.Join(root, "server.ini"), []byte("ServerName=Old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	maxLength := 64
	definition := templates.ConfigAdapterDefinition{SchemaVersion: 1, ID: "file-settings", Version: "1.0.0", Format: templates.FormatINIKeyValues, Target: "server.ini", RestartRequired: true, Fields: []templates.ConfigAdapterField{{Key: "SERVER_NAME", Label: "Server name", Type: "string", Property: "ServerName", Required: true, Validation: templates.Validation{MaxLength: &maxLength}}}}
	raw, _ := json.Marshal(definition)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	record, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "File", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate}, "file-game", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: 1, Version: definition.Version, TemplateID: "file-game", TemplateVersion: "1.0.0", DefinitionJSON: raw}})
	if err != nil {
		t.Fatal(err)
	}
	service := gameconfig.New(db, serverService)
	arguments, environment, err := service.ResolveLaunch(context.Background(), record.Server.ID, []string{"-base"}, map[string]string{"KEEP": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) != 1 || arguments[0] != "-base" || len(environment) != 1 {
		t.Fatalf("a file adapter must not contribute launch data: %#v %#v", arguments, environment)
	}
}

// TestResolveLaunchUsesOnlyThePersistedSnapshot proves the runtime resolver
// reads the per-server adapter snapshot from SQLite and never the current
// Official catalog. The pinned snapshot deliberately diverges from the shipped
// repository adapter, and resolution must follow the snapshot.
func TestResolveLaunchUsesOnlyThePersistedSnapshot(t *testing.T) {
	repository, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "valheim", "valheim-settings.adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var current templates.ConfigAdapterDefinition
	if err = json.Unmarshal(repository, &current); err != nil {
		t.Fatal(err)
	}
	// The catalog adapter shipped in this repository binds SERVER_NAME to -name.
	catalogArgument := ""
	for _, field := range current.Fields {
		if field.Key == "SERVER_NAME" {
			catalogArgument = field.Binding.Argument
		}
	}
	if catalogArgument != "-name" {
		t.Fatalf("unexpected repository adapter binding: %q", catalogArgument)
	}

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
	// Pin an older snapshot that used a different argument name.
	pinned := launchDefinition()
	pinned.ID = "valheim-settings"
	pinned.Version = "0.9.0"
	pinned.Fields[0].Binding = &templates.ConfigAdapterBinding{Type: templates.BindingLaunchValue, Argument: "-legacyname"}
	raw, _ := json.Marshal(pinned)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	record, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "Pinned Snapshot", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate}, "valheim", nil, nil, []servers.ProvisionedConfigAdapter{{ID: pinned.ID, SchemaVersion: pinned.SchemaVersion, Version: pinned.Version, TemplateID: "valheim", TemplateVersion: "1.1.0", DefinitionJSON: raw, Values: fullValues()}})
	if err != nil {
		t.Fatal(err)
	}
	service := gameconfig.New(db, serverService)
	arguments, _, err := service.ResolveLaunch(context.Background(), record.Server.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, " ")
	if !strings.Contains(joined, "-legacyname My Valheim") {
		t.Fatalf("resolution did not use the pinned snapshot: %#v", arguments)
	}
	if strings.Contains(joined, "-name My Valheim") {
		t.Fatalf("resolution consulted the current catalog adapter: %#v", arguments)
	}
	result, err := service.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Adapters[0].Version != "0.9.0" {
		t.Fatalf("configuration view did not use the pinned snapshot: %s", result.Adapters[0].Version)
	}

	// The persisted row is the only input: changing it changes resolution.
	updated := pinned
	updated.Fields[0].Binding = &templates.ConfigAdapterBinding{Type: templates.BindingLaunchValue, Argument: "-renamed"}
	updatedRaw, _ := json.Marshal(updated)
	if _, err = db.Exec(`UPDATE server_config_adapters SET definition_json=? WHERE server_id=?`, string(updatedRaw), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	arguments, _, err = service.ResolveLaunch(context.Background(), record.Server.ID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(arguments, " "), "-renamed My Valheim") {
		t.Fatalf("the persisted snapshot is not authoritative: %#v", arguments)
	}
}
