package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	"gamenode/internal/tenants"
)

type crossTenantFixture struct {
	provisionAPIFixture
	tenantA, tenantB          tenants.Tenant
	userA, userB              identity.User
	sessionA, sessionB, admin testSession
	serverA, serverB          string
	serverARoot, serverBRoot  string
}

// newCrossTenantFixture builds two fully separate tenants, each with one
// user holding a tenant-scoped "Server Operator" role and one server, on top
// of the existing provisioning API fixture so the same setup also covers
// Arbeitsschritt 4's provisioning authorization tests.
func newCrossTenantFixture(t *testing.T) *crossTenantFixture {
	t.Helper()
	fixture := newProvisionAPI(t, &apiInstaller{})
	ctx := context.Background()

	tenantService := tenants.New(fixture.db)
	tenantA, err := tenantService.Create(ctx, tenants.CreateInput{Name: "Tenant A"})
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := tenantService.Create(ctx, tenants.CreateInput{Name: "Tenant B"})
	if err != nil {
		t.Fatal(err)
	}

	identities := identity.New(fixture.db)
	userA, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "user-a", Email: "user-a@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	userB, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "user-b", Email: "user-b@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}

	authorization := rbac.New(fixture.db)
	operator, err := authorization.CreateRole(ctx, "Tenant Server Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, operator.ID, []string{"Server.View", "Server.Edit", "Server.Delete", "Server.Start", "Server.Stop", "Server.Restart", "Server.Kill", "Console.View", "Console.Send", "Files.View", "Files.Edit", "Files.Upload", "Files.Download", "Files.Delete", "Files.Rename", "Ports.View", "Ports.Manage", "Monitoring.View"}); err != nil {
		t.Fatal(err)
	}
	if err = authorization.AssignUser(ctx, userA.ID, operator.ID, rbac.Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	if err = authorization.AssignUser(ctx, userB.ID, operator.ID, rbac.Scope{Type: "tenant", ID: &tenantB.ID}); err != nil {
		t.Fatal(err)
	}

	admin := createAdminSession(t, fixture.handler)
	sessionA := loginSession(t, fixture.handler, userA.Username)
	sessionB := loginSession(t, fixture.handler, userB.Username)

	rootA := t.TempDir()
	rootB := t.TempDir()
	if err = os.WriteFile(filepath.Join(rootA, "readme.txt"), []byte("server a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(rootB, "readme.txt"), []byte("server b"), 0o644); err != nil {
		t.Fatal(err)
	}
	serverA := createServerInRoot(t, fixture.handler, admin, tenantA.ID, "Server A", rootA)
	serverB := createServerInRoot(t, fixture.handler, admin, tenantB.ID, "Server B", rootB)

	return &crossTenantFixture{
		provisionAPIFixture: fixture,
		tenantA:             tenantA, tenantB: tenantB,
		userA: userA, userB: userB,
		sessionA: sessionA, sessionB: sessionB, admin: admin,
		serverA: serverA, serverB: serverB,
		serverARoot: rootA, serverBRoot: rootB,
	}
}

func createServerInRoot(t *testing.T, handler http.Handler, admin testSession, tenantID, name, root string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"tenant_id":             tenantID,
		"creation_mode":         "custom",
		"name":                  name,
		"working_directory":     filepath.ToSlash(root),
		"executable":            filepath.ToSlash(executable),
		"arguments":             []string{},
		"environment_variables": map[string]string{},
		"stop_timeout_seconds":  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := templateRequest(handler, http.MethodPost, "/api/v1/servers", payload, &admin, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("create server: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.Server.ID
}

// TestCrossTenantServerSubsystemIsolation is the required regression suite:
// User A, holding only a Tenant A role, must be denied every server-ID-based
// operation against Server B (owned by Tenant B), with a uniform 403 that
// never distinguishes "wrong tenant" from "does not exist". The same calls
// against User A's own Server A must not be blocked by RBAC, proving the
// denial is genuinely tenant-scoped rather than a blanket lockout.
func TestCrossTenantServerSubsystemIsolation(t *testing.T) {
	fixture := newCrossTenantFixture(t)

	type call struct {
		name   string
		method string
		path   func(server string) string
		body   []byte
		csrf   bool
	}
	calls := []call{
		{"detail", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id }, nil, false},
		{"edit", http.MethodPatch, func(id string) string { return "/api/v1/servers/" + id }, []byte(`{"name":"Renamed"}`), true},
		{"start", http.MethodPost, func(id string) string { return "/api/v1/servers/" + id + "/start" }, nil, true},
		{"stop", http.MethodPost, func(id string) string { return "/api/v1/servers/" + id + "/stop" }, nil, true},
		{"restart", http.MethodPost, func(id string) string { return "/api/v1/servers/" + id + "/restart" }, nil, true},
		{"kill", http.MethodPost, func(id string) string { return "/api/v1/servers/" + id + "/kill" }, nil, true},
		{"files list", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id + "/files?path=." }, nil, false},
		{"files content", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id + "/files/content?path=readme.txt" }, nil, false},
		{"files content write", http.MethodPut, func(id string) string { return "/api/v1/servers/" + id + "/files/content" }, []byte(`{"path":"readme.txt","content":"pwned"}`), true},
		{"files create", http.MethodPost, func(id string) string { return "/api/v1/servers/" + id + "/files/file" }, []byte(`{"path":"new.txt","content":"pwned"}`), true},
		{"files move", http.MethodPost, func(id string) string { return "/api/v1/servers/" + id + "/files/move" }, []byte(`{"source":"readme.txt","destination":"moved.txt"}`), true},
		{"files delete", http.MethodDelete, func(id string) string { return "/api/v1/servers/" + id + "/files?path=readme.txt" }, nil, true},
		{"files download", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id + "/files/download?path=readme.txt" }, nil, false},
		{"files upload", http.MethodPost, func(id string) string { return "/api/v1/servers/" + id + "/files/upload?path=." }, []byte("not-a-real-multipart-body"), true},
		{"configuration read", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id + "/configuration" }, nil, false},
		{"configuration write", http.MethodPut, func(id string) string { return "/api/v1/servers/" + id + "/configuration" }, []byte(`{"adapter_id":"x","values":{}}`), true},
		{"ports list", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id + "/ports" }, nil, false},
		{"ports create", http.MethodPost, func(id string) string { return "/api/v1/servers/" + id + "/ports" }, []byte(`{"name":"Game","protocol":"tcp","bind_address":"127.0.0.1","port":26900}`), true},
		{"monitoring", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id + "/monitoring" }, nil, false},
		{"monitoring history", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id + "/monitoring/history" }, nil, false},
		{"console websocket", http.MethodGet, func(id string) string { return "/api/v1/servers/" + id + "/console/ws" }, nil, false},
		{"delete", http.MethodDelete, func(id string) string { return "/api/v1/servers/" + id }, nil, true},
	}

	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			response := templateRequest(fixture.handler, c.method, c.path(fixture.serverB), c.body, &fixture.sessionA, c.csrf)
			if response.Code != http.StatusForbidden {
				t.Fatalf("User A -> Server B %s = %d, want 403 Forbidden: %s", c.name, response.Code, response.Body.String())
			}
		})
	}

	// Server access listing requires global Roles.View, which neither user
	// holds; both A-on-A and A-on-B are denied identically, so this alone
	// cannot leak whether B differs from A.
	for _, id := range []string{fixture.serverA, fixture.serverB} {
		response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/"+id+"/access", nil, &fixture.sessionA, false)
		if response.Code != http.StatusForbidden {
			t.Fatalf("server access listing without Roles.View = %d: %s", response.Code, response.Body.String())
		}
	}

	// Server B must be completely unaffected: still present, still named
	// "Server B", still owns its file unchanged.
	adminGet := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/"+fixture.serverB, nil, &fixture.admin, false)
	if adminGet.Code != http.StatusOK || !strings.Contains(adminGet.Body.String(), "Server B") {
		t.Fatalf("server B survived: %d %s", adminGet.Code, adminGet.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(fixture.serverBRoot, "readme.txt"))
	if err != nil || string(content) != "server b" {
		t.Fatalf("server B file content changed: %q err=%v", content, err)
	}

	// Positive control: the exact same requests against User A's own Server
	// A are not blocked by RBAC (proves this is tenant-scoped access, not a
	// blanket deny). Only read-oriented and idempotent calls are exercised
	// here to avoid mutating state needed elsewhere.
	for _, c := range []call{calls[0], calls[6], calls[7], calls[12], calls[15], calls[18], calls[19]} {
		response := templateRequest(fixture.handler, c.method, c.path(fixture.serverA), c.body, &fixture.sessionA, c.csrf)
		if response.Code == http.StatusForbidden {
			t.Fatalf("User A -> Server A %s unexpectedly denied: %d %s", c.name, response.Code, response.Body.String())
		}
	}
}

// TestCrossTenantDashboardIsolation covers item 14: User A must not be able
// to infer Tenant B's server count or any other aggregate from the
// dashboard, even though Tenant B legitimately owns more servers than
// Tenant A.
func TestCrossTenantDashboardIsolation(t *testing.T) {
	fixture := newCrossTenantFixture(t)
	// Give Tenant B several more servers than Tenant A.
	for i := 0; i < 5; i++ {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		createServerInRoot(t, fixture.handler, fixture.admin, fixture.tenantB.ID, fmt.Sprintf("Extra B Server %d", i), root)
	}

	response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/dashboard", nil, &fixture.sessionA, false)
	if response.Code != http.StatusOK {
		t.Fatalf("dashboard: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Servers map[string]int `json:"servers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Servers["total"] != 1 {
		t.Fatalf("User A dashboard total = %d, want 1 (only Server A visible, not Tenant B's 6 servers)", payload.Servers["total"])
	}

	adminResponse := templateRequest(fixture.handler, http.MethodGet, "/api/v1/dashboard", nil, &fixture.admin, false)
	var adminPayload struct {
		Servers map[string]int `json:"servers"`
	}
	if err := json.Unmarshal(adminResponse.Body.Bytes(), &adminPayload); err != nil {
		t.Fatal(err)
	}
	if adminPayload.Servers["total"] != 7 {
		t.Fatalf("admin dashboard total = %d, want 7 (both tenants' servers)", adminPayload.Servers["total"])
	}

	// The response body itself must never mention Server B's ID/name for
	// User A, confirming no leakage through any auxiliary field.
	if strings.Contains(response.Body.String(), fixture.serverB) || strings.Contains(response.Body.String(), "Extra B Server") {
		t.Fatalf("dashboard leaked Tenant B data: %s", response.Body.String())
	}
}

// TestCrossTenantProvisioningAuthorization covers item 15: a user with
// Server.Create scoped to Tenant A only.
func TestCrossTenantProvisioningAuthorization(t *testing.T) {
	fixture := newCrossTenantFixture(t)
	ctx := context.Background()
	identities := identity.New(fixture.db)
	authorization := rbac.New(fixture.db)

	creatorUser, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "tenant-a-creator", Email: "tenant-a-creator@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := authorization.CreateRole(ctx, "Tenant A Creator", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, role.ID, []string{"Server.Create"}); err != nil {
		t.Fatal(err)
	}
	if err = authorization.AssignUser(ctx, creatorUser.ID, role.ID, rbac.Scope{Type: "tenant", ID: &fixture.tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	session := loginSession(t, fixture.handler, creatorUser.Username)
	path := "/api/v1/templates/" + fixture.template.ID + "/provision"

	// provision Tenant A -> allowed (Templates.View is missing though, so
	// this specific user also needs it; grant it to isolate the Server.Create
	// scope behavior being tested here).
	if err = authorization.AssignUser(ctx, creatorUser.ID, mustGlobalTemplatesViewRole(t, authorization), rbac.Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}

	allowedBody, _ := json.Marshal(map[string]any{"tenant_id": fixture.tenantA.ID, "server_name": "A Provisioned", "directory_name": "a-provisioned", "variables": map[string]string{}})
	if response := templateRequest(fixture.handler, http.MethodPost, path, allowedBody, &session, true); response.Code != http.StatusAccepted {
		t.Fatalf("provision Tenant A = %d %s, want 202 Accepted", response.Code, response.Body.String())
	}

	// provision Tenant B -> denied.
	deniedBody, _ := json.Marshal(map[string]any{"tenant_id": fixture.tenantB.ID, "server_name": "B Provisioned", "directory_name": "b-provisioned", "variables": map[string]string{}})
	if response := templateRequest(fixture.handler, http.MethodPost, path, deniedBody, &session, true); response.Code != http.StatusForbidden {
		t.Fatalf("provision Tenant B = %d %s, want 403 Forbidden", response.Code, response.Body.String())
	}

	// unknown tenant for the tenant-scoped user: RBAC denies it before
	// provisioning's own tenant-existence check is ever reached (their
	// tenant assignment cannot match an ID that names no tenant), which is
	// itself a controlled 403 that leaks nothing about whether the tenant
	// exists.
	unknownBody, _ := json.Marshal(map[string]any{"tenant_id": "does-not-exist", "server_name": "Unknown Tenant", "directory_name": "unknown-tenant", "variables": map[string]string{}})
	if response := templateRequest(fixture.handler, http.MethodPost, path, unknownBody, &session, true); response.Code != http.StatusForbidden {
		t.Fatalf("provision unknown tenant (tenant-scoped user) = %d %s, want 403 Forbidden", response.Code, response.Body.String())
	}
	// unknown tenant for a global-privileged user: RBAC passes (a global
	// grant matches any tenant ID), so this reaches provisioning's own
	// tenant-existence validation, which reports a distinct controlled 400
	// rather than a 500 or a silent success.
	if response := templateRequest(fixture.handler, http.MethodPost, path, unknownBody, &fixture.admin, true); response.Code != http.StatusBadRequest {
		t.Fatalf("provision unknown tenant (admin) = %d %s, want 400 Bad Request", response.Code, response.Body.String())
	}

	// Membership in Tenant B alone (no role assignment there) still denied:
	// internal/tenants.Membership carries no RBAC weight.
	if _, err = tenantMembershipService(fixture).AddMember(ctx, fixture.tenantB.ID, creatorUser.ID); err != nil {
		t.Fatal(err)
	}
	if response := templateRequest(fixture.handler, http.MethodPost, path, deniedBody, &session, true); response.Code != http.StatusForbidden {
		t.Fatalf("provision Tenant B via membership alone = %d %s, want 403 Forbidden", response.Code, response.Body.String())
	}

	// Arbitrary host path via managed provisioning is impossible: the
	// request body has no field for it at all (only tenant_id/directory_name),
	// so this is a structural guarantee rather than something to probe with
	// a payload. Custom/Adopt Existing (POST /servers), which does accept a
	// working_directory, remains denied for this tenant-scoped-only user
	// since it still requires global Server.Create.
	customBody, _ := json.Marshal(map[string]any{"tenant_id": fixture.tenantA.ID, "creation_mode": "adopt", "name": "Adopted", "working_directory": fixture.serverARoot, "executable": mustExecutable(t), "arguments": []string{}, "environment_variables": map[string]string{}, "stop_timeout_seconds": 1})
	if response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/servers", customBody, &session, true); response.Code != http.StatusForbidden {
		t.Fatalf("tenant-scoped Server.Create used Custom/Adopt path = %d %s, want 403 Forbidden", response.Code, response.Body.String())
	}
}

func mustExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(executable)
}

func mustGlobalTemplatesViewRole(t *testing.T, authorization *rbac.Service) string {
	t.Helper()
	role, err := authorization.CreateRole(context.Background(), "Global Templates Viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(context.Background(), role.ID, []string{"Templates.View"}); err != nil {
		t.Fatal(err)
	}
	return role.ID
}

func tenantMembershipService(fixture *crossTenantFixture) *tenants.Service {
	return tenants.New(fixture.db)
}

// TestCreatableTenantsEndpointReflectsServerCreateScope covers the Step 5 UI
// support endpoint (GET /servers/creatable-tenants): it must never require
// Tenants.View, and must return exactly the tenants where Server.Create is
// effective for the caller - not more (no accidental broad tenant listing)
// and not fewer (tenant-scoped grants must appear).
func TestCreatableTenantsEndpointReflectsServerCreateScope(t *testing.T) {
	fixture := newCrossTenantFixture(t)
	ctx := context.Background()
	identities := identity.New(fixture.db)
	authorization := rbac.New(fixture.db)

	// A user with no Server.Create anywhere sees no tenants.
	nobody, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "nobody", Email: "nobody@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	nobodySession := loginSession(t, fixture.handler, nobody.Username)
	nobodyResponse := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/creatable-tenants", nil, &nobodySession, false)
	if nobodyResponse.Code != http.StatusOK {
		t.Fatalf("creatable-tenants without Server.Create: %d %s", nobodyResponse.Code, nobodyResponse.Body.String())
	}
	var nobodyPayload struct {
		Tenants []tenants.Tenant `json:"tenants"`
	}
	if err = json.Unmarshal(nobodyResponse.Body.Bytes(), &nobodyPayload); err != nil {
		t.Fatal(err)
	}
	if len(nobodyPayload.Tenants) != 0 {
		t.Fatalf("nobody's creatable tenants = %#v, want none", nobodyPayload.Tenants)
	}

	// A tenant-scoped Server.Create grant returns exactly that tenant, even
	// though this user has no Tenants.View at all.
	tenantScoped, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "tenant-scoped-creator", Email: "tenant-scoped-creator@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := authorization.CreateRole(ctx, "Creator Role", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, role.ID, []string{"Server.Create"}); err != nil {
		t.Fatal(err)
	}
	if err = authorization.AssignUser(ctx, tenantScoped.ID, role.ID, rbac.Scope{Type: "tenant", ID: &fixture.tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	tenantScopedSession := loginSession(t, fixture.handler, tenantScoped.Username)
	scopedResponse := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/creatable-tenants", nil, &tenantScopedSession, false)
	if scopedResponse.Code != http.StatusOK {
		t.Fatalf("creatable-tenants for tenant-scoped user: %d %s", scopedResponse.Code, scopedResponse.Body.String())
	}
	var scopedPayload struct {
		Tenants []tenants.Tenant `json:"tenants"`
	}
	if err = json.Unmarshal(scopedResponse.Body.Bytes(), &scopedPayload); err != nil {
		t.Fatal(err)
	}
	if len(scopedPayload.Tenants) != 1 || scopedPayload.Tenants[0].ID != fixture.tenantA.ID {
		t.Fatalf("tenant-scoped creatable tenants = %#v, want exactly [%s]", scopedPayload.Tenants, fixture.tenantA.ID)
	}

	// A global Server.Create grant (admin bypass here) returns every tenant.
	adminResponse := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/creatable-tenants", nil, &fixture.admin, false)
	var adminPayload struct {
		Tenants []tenants.Tenant `json:"tenants"`
	}
	if err = json.Unmarshal(adminResponse.Body.Bytes(), &adminPayload); err != nil {
		t.Fatal(err)
	}
	if len(adminPayload.Tenants) < 2 {
		t.Fatalf("admin creatable tenants = %#v, want at least Tenant A and Tenant B", adminPayload.Tenants)
	}
}
