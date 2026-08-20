package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/tenants"
)

func newMigrationTestServer(t *testing.T) (http.Handler, *sql.DB, string) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	dataRoot := t.TempDir()
	service := servers.NewService(servers.NewStore(db), runtime.NewNative())
	handler := api.New(auth.New(db), service, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), false, api.Options{DataDirectory: dataRoot}).Handler(http.NotFoundHandler())
	return handler, db, dataRoot
}

func TestAdministratorCanMigrateStoppedServerBetweenTenants(t *testing.T) {
	h, db, dataRoot := newMigrationTestServer(t)
	admin := createAdminSession(t, h)
	createdTenant := templateRequest(h, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"Destination"}`), &admin, true)
	if createdTenant.Code != http.StatusCreated {
		t.Fatalf("create destination tenant: %d %s", createdTenant.Code, createdTenant.Body.String())
	}
	var tenantBody struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
	}
	if err := json.Unmarshal(createdTenant.Body.Bytes(), &tenantBody); err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := tenants.TenantServerRoot(dataRoot, tenants.DefaultTenantID, "migratable")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(sourceRoot, "server.exe")
	if err = os.WriteFile(executable, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	created, err := servers.NewStore(db).CreateProvisioned(context.Background(), servers.Server{TenantID: tenants.DefaultTenantID, CreationMode: servers.CreationTemplate, Name: "Migratable server", WorkingDirectory: sourceRoot, Executable: executable, Arguments: []string{}, EnvironmentVariables: map[string]string{}, StopTimeoutSeconds: 1}, "test-template", nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverID := created.Server.ID
	migrated := templateRequest(h, http.MethodPost, "/api/v1/servers/"+serverID+"/tenant", []byte(`{"tenant_id":"`+tenantBody.Tenant.ID+`"}`), &admin, true)
	if migrated.Code != http.StatusOK {
		t.Fatalf("migrate server: %d %s", migrated.Code, migrated.Body.String())
	}
	var body struct {
		Server struct {
			TenantID         string `json:"tenant_id"`
			WorkingDirectory string `json:"working_directory"`
		} `json:"server"`
	}
	if err := json.Unmarshal(migrated.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Server.TenantID != tenantBody.Tenant.ID {
		t.Fatalf("migrated tenant_id = %q, want %q", body.Server.TenantID, tenantBody.Tenant.ID)
	}
	destinationRoot, err := tenants.TenantServerRoot(dataRoot, tenantBody.Tenant.ID, "migratable")
	if err != nil {
		t.Fatal(err)
	}
	if body.Server.WorkingDirectory != destinationRoot {
		t.Fatalf("migrated working_directory = %q, want %q", body.Server.WorkingDirectory, destinationRoot)
	}
	var stored string
	if err := db.QueryRow(`SELECT tenant_id FROM servers WHERE id=?`, serverID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != tenantBody.Tenant.ID {
		t.Fatalf("stored tenant_id = %q, want %q", stored, tenantBody.Tenant.ID)
	}
	events, err := audit.New(db).List(context.Background(), audit.Filter{Action: audit.ServerTenantMigrate, ServerID: &serverID})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Result != audit.Success {
		t.Fatalf("migration audit events = %#v", events)
	}
}
