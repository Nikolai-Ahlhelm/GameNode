package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/gameconfig"
	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

const apiLaunchSecret = "API_LAUNCH_SECRET_SHOULD_NEVER_APPEAR"

func managedLaunchDefinition() templates.ConfigAdapterDefinition {
	maxLength := 64
	return templates.ConfigAdapterDefinition{
		SchemaVersion:   templates.AdapterSchemaVersion,
		ID:              "valheim-settings",
		Version:         "1.0.0",
		Format:          templates.FormatManagedLaunch,
		RestartRequired: true,
		Fields: []templates.ConfigAdapterField{
			{Key: "SERVER_NAME", Label: "Server name", Section: "General", Type: "string", Required: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchValue, Argument: "-name"}},
			{Key: "SERVER_PASSWORD", Label: "Server password", Section: "Security", Type: "secret", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchSecret, Argument: "-password"}},
			{Key: "API_TOKEN", Label: "API token", Section: "Security", Type: "secret", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingEnvironmentSecret, Name: "GAME_TOKEN"}},
			{Key: "CROSSPLAY", Label: "Crossplay", Section: "Networking", Type: "boolean", Required: true, Binding: &templates.ConfigAdapterBinding{Type: templates.BindingLaunchFlag, Argument: "-crossplay"}},
		},
	}
}

// TestManagedLaunchConfigurationAPISecrecy covers the full transport surface:
// CSRF, secret redaction on read, audit metadata, and the server DTO.
func TestManagedLaunchConfigurationAPISecrecy(t *testing.T) {
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
	definition := managedLaunchDefinition()
	raw, _ := json.Marshal(definition)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	configService := gameconfig.New(db, serverService)
	record, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "Valheim API", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate, Arguments: []string{"-nographics"}}, "valheim", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: definition.SchemaVersion, Version: definition.Version, TemplateID: "valheim", TemplateVersion: "1.1.0", DefinitionJSON: raw, Values: []servers.ProvisionedConfigValue{{Key: "SERVER_NAME", Value: "My Valheim"}, {Key: "CROSSPLAY", Value: "1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{GameConfig: configService}).Handler(http.NotFoundHandler())
	admin := createAdminSession(t, handler)
	path := "/api/v1/servers/" + record.Server.ID + "/configuration"

	initial := templateRequest(handler, http.MethodGet, path, nil, &admin, false)
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), `"available":true`) || !strings.Contains(initial.Body.String(), `"restart_required":true`) {
		t.Fatalf("get=%d %s", initial.Code, initial.Body.String())
	}
	body := []byte(`{"adapter_id":"valheim-settings","values":{"SERVER_PASSWORD":"` + apiLaunchSecret + `","API_TOKEN":"` + apiLaunchSecret + `"}}`)
	if response := templateRequest(handler, http.MethodPut, path, body, &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("csrf must be required: %d", response.Code)
	}
	updated := templateRequest(handler, http.MethodPut, path, body, &admin, true)
	if updated.Code != http.StatusOK {
		t.Fatalf("update=%d %s", updated.Code, updated.Body.String())
	}
	if strings.Contains(updated.Body.String(), apiLaunchSecret) {
		t.Fatalf("PUT response leaked the secret: %s", updated.Body.String())
	}
	after := templateRequest(handler, http.MethodGet, path, nil, &admin, false)
	if strings.Contains(after.Body.String(), apiLaunchSecret) {
		t.Fatalf("GET leaked the secret: %s", after.Body.String())
	}
	if !strings.Contains(after.Body.String(), `"key":"SERVER_PASSWORD","label":"Server password"`) || !strings.Contains(after.Body.String(), `"configured":true`) {
		t.Fatalf("secret must be reported as configured: %s", after.Body.String())
	}
	detail := templateRequest(handler, http.MethodGet, "/api/v1/servers/"+record.Server.ID, nil, &admin, false)
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), apiLaunchSecret) {
		t.Fatalf("server DTO leaked the secret: %d %s", detail.Code, detail.Body.String())
	}
	// The secret must never be persisted into the resolved base launch.
	stored, err := serverService.Get(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(stored.Server.Arguments, " "), apiLaunchSecret) {
		t.Fatalf("secret was persisted into server arguments: %#v", stored.Server.Arguments)
	}
	for _, value := range stored.Server.EnvironmentVariables {
		if value == apiLaunchSecret {
			t.Fatal("secret was persisted into the server environment")
		}
	}
	// It must only exist in the resolved runtime launch.
	arguments, environment, err := configService.ResolveLaunch(context.Background(), record.Server.ID, stored.Server.Arguments, stored.Server.EnvironmentVariables)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(arguments, " "), apiLaunchSecret) || environment["GAME_TOKEN"] != apiLaunchSecret {
		t.Fatalf("resolved launch is missing the configured secret: %#v %#v", arguments, environment)
	}
	var auditText string
	if err = db.QueryRow(`SELECT COALESCE(GROUP_CONCAT(COALESCE(metadata_json,'')||COALESCE(error_summary,'')),'') FROM audit_log`).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditText, apiLaunchSecret) {
		t.Fatalf("audit leaked the secret: %q", auditText)
	}
	if !strings.Contains(auditText, "valheim-settings") {
		t.Fatalf("configuration audit event is missing: %q", auditText)
	}
}

// TestManagedLaunchConfigurationRequiresServerEdit proves the read and write
// permissions are enforced independently by the backend.
func TestManagedLaunchConfigurationRequiresServerEdit(t *testing.T) {
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
	definition := managedLaunchDefinition()
	raw, _ := json.Marshal(definition)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	record, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "Valheim RBAC", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate}, "valheim", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: definition.SchemaVersion, Version: definition.Version, TemplateID: "valheim", TemplateVersion: "1.1.0", DefinitionJSON: raw, Values: []servers.ProvisionedConfigValue{{Key: "SERVER_NAME", Value: "My Valheim"}, {Key: "CROSSPLAY", Value: "0"}}}})
	if err != nil {
		t.Fatal(err)
	}
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{GameConfig: gameconfig.New(db, serverService)}).Handler(http.NotFoundHandler())
	admin := createAdminSession(t, handler)
	// A user holding only server-scoped Server.View must read but not write.
	identities := identity.New(db)
	viewerUser := createRBACUser(t, identities, "config-viewer")
	authorization := rbac.New(db)
	viewerRole := createRBACRole(t, authorization, "Configuration Viewer", []string{"Server.View"})
	assignment, _ := json.Marshal(map[string]any{"role_id": viewerRole.ID, "scope_type": "server", "scope_id": record.Server.ID})
	if response := templateRequest(handler, http.MethodPost, "/api/v1/users/"+viewerUser.ID+"/roles", assignment, &admin, true); response.Code != http.StatusCreated {
		t.Fatalf("assignment=%d %s", response.Code, response.Body.String())
	}
	viewer := loginSession(t, handler, "config-viewer")
	path := "/api/v1/servers/" + record.Server.ID + "/configuration"
	if response := templateRequest(handler, http.MethodGet, path, nil, &viewer, false); response.Code != http.StatusOK {
		t.Fatalf("Server.View must allow reading configuration: %d %s", response.Code, response.Body.String())
	}
	body := []byte(`{"adapter_id":"valheim-settings","values":{"SERVER_NAME":"Renamed"}}`)
	if response := templateRequest(handler, http.MethodPut, path, body, &viewer, true); response.Code != http.StatusForbidden {
		t.Fatalf("Server.View must not allow writing configuration: %d %s", response.Code, response.Body.String())
	}
}
