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
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

func TestManagedGameConfigurationAPICSRFSecretsAuditAndSnapshot(t *testing.T) {
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
	if err = os.WriteFile(filepath.Join(root, "serverconfig.xml"), []byte(`<ServerSettings><property name="ServerName" value="Old"/><property name="ServerPassword" value="old-secret"/></ServerSettings>`), 0600); err != nil {
		t.Fatal(err)
	}
	maxLength := 128
	definition := templates.ConfigAdapterDefinition{SchemaVersion: 1, ID: "serverconfig", Version: "1.0.0", Format: "xml-properties", Target: "serverconfig.xml", RestartRequired: true, Fields: []templates.ConfigAdapterField{{Key: "NAME", Label: "Server name", Type: "string", Property: "ServerName", Required: true, Validation: templates.Validation{MaxLength: &maxLength}}, {Key: "PASSWORD", Label: "Password", Type: "secret", Property: "ServerPassword", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}}}}
	raw, _ := json.Marshal(definition)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	record, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "Managed", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate}, "template", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: 1, Version: definition.Version, TemplateID: "template", TemplateVersion: "1.0.0", DefinitionJSON: raw}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{GameConfig: gameconfig.New(db, serverService)}).Handler(http.NotFoundHandler())
	admin := createAdminSession(t, handler)
	path := "/api/v1/servers/" + record.Server.ID + "/configuration"
	get := templateRequest(handler, http.MethodGet, path, nil, &admin, false)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"available":true`) || strings.Contains(get.Body.String(), "old-secret") {
		t.Fatalf("get=%d %s", get.Code, get.Body.String())
	}
	body := []byte(`{"adapter_id":"serverconfig","values":{"NAME":"New & Safe","PASSWORD":"NEW_SECRET"}}`)
	if response := templateRequest(handler, http.MethodPut, path, body, &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("csrf=%d", response.Code)
	}
	updated := templateRequest(handler, http.MethodPut, path, body, &admin, true)
	if updated.Code != http.StatusOK || strings.Contains(updated.Body.String(), "NEW_SECRET") {
		t.Fatalf("update=%d %s", updated.Code, updated.Body.String())
	}
	values, err := gameconfig.Read(root, definition)
	if err != nil || values["NAME"] != "New & Safe" || values["PASSWORD"] != "NEW_SECRET" {
		t.Fatalf("values=%#v err=%v", values, err)
	}
	var auditText string
	if err = db.QueryRow(`SELECT COALESCE(metadata_json,'')||COALESCE(error_summary,'') FROM audit_log WHERE action='server.config_update'`).Scan(&auditText); err != nil || strings.Contains(auditText, "NEW_SECRET") || !strings.Contains(auditText, "serverconfig") {
		t.Fatalf("audit=%q err=%v", auditText, err)
	}
}

func TestManagedPostStartINIConfigurationPendingThenAvailable(t *testing.T) {
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
	definition := templates.ConfigAdapterDefinition{SchemaVersion: 1, ID: "project-zomboid-server-ini", Version: "1.0.0", Format: "ini-key-values", Target: "Server/gamenode.ini", RestartRequired: true, PostStartOnly: true, Fields: []templates.ConfigAdapterField{{Key: "PZ_PUBLIC_NAME", Label: "Public name", Type: "string", Property: "PublicName", Required: true, Validation: templates.Validation{MaxLength: &maxLength}}, {Key: "PZ_PASSWORD", Label: "Password", Type: "secret", Property: "Password", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}}}}
	raw, _ := json.Marshal(definition)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	record, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "PZ", WorkingDirectory: root, Executable: "game.exe", RuntimeType: "native", CreationMode: servers.CreationTemplate}, "project-zomboid", nil, nil, []servers.ProvisionedConfigAdapter{{ID: definition.ID, SchemaVersion: 1, Version: definition.Version, TemplateID: "project-zomboid", TemplateVersion: "1.1.0", DefinitionJSON: raw}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{GameConfig: gameconfig.New(db, serverService)}).Handler(http.NotFoundHandler())
	admin := createAdminSession(t, handler)
	path := "/api/v1/servers/" + record.Server.ID + "/configuration"
	pending := templateRequest(handler, http.MethodGet, path, nil, &admin, false)
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), `"available":true`) || !strings.Contains(pending.Body.String(), `"ready":false`) || !strings.Contains(pending.Body.String(), "Start this server once") {
		t.Fatalf("pending=%d %s", pending.Code, pending.Body.String())
	}
	update := []byte(`{"adapter_id":"project-zomboid-server-ini","values":{"PZ_PUBLIC_NAME":"Before start"}}`)
	if response := templateRequest(handler, http.MethodPut, path, update, &admin, true); response.Code != http.StatusNotFound {
		t.Fatalf("pending update=%d %s", response.Code, response.Body.String())
	}
	if err = os.MkdirAll(filepath.Join(root, "Server"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "Server", "gamenode.ini"), []byte("PublicName=Generated\nPassword=\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ready := templateRequest(handler, http.MethodGet, path, nil, &admin, false)
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"ready":true`) || !strings.Contains(ready.Body.String(), `"value":"Generated"`) {
		t.Fatalf("ready=%d %s", ready.Code, ready.Body.String())
	}
	injected := []byte("{\"adapter_id\":\"project-zomboid-server-ini\",\"values\":{\"PZ_PUBLIC_NAME\":\"safe\\nPublic=true\"}}")
	if response := templateRequest(handler, http.MethodPut, path, injected, &admin, true); response.Code != http.StatusBadRequest {
		t.Fatalf("injection=%d %s", response.Code, response.Body.String())
	}
}
