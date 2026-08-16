package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	"gamenode/internal/tenants"
)

func TestTenantsAPICRUDAuthorizationAndCSRF(t *testing.T) {
	handler, db := newTestServer(t)
	admin := createAdminSession(t, handler)
	ctx := context.Background()
	identities := identity.New(db)
	authorization := rbac.New(db)

	// A plain authenticated user with no Tenants.View/Manage is denied
	// reading or mutating tenant entities.
	viewerUser, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "no-tenant-access", Email: "no-tenant-access@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	noAccessSession := loginSession(t, handler, viewerUser.Username)
	if response := templateRequest(handler, http.MethodGet, "/api/v1/tenants", nil, &noAccessSession, false); response.Code != http.StatusForbidden {
		t.Fatalf("list without Tenants.View: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(handler, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"Nope"}`), &noAccessSession, true); response.Code != http.StatusForbidden {
		t.Fatalf("create without Tenants.Manage: %d %s", response.Code, response.Body.String())
	}

	// A user with only Tenants.View can read but not mutate.
	viewerRole, err := authorization.CreateRole(ctx, "Tenant Viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, viewerRole.ID, []string{"Tenants.View"}); err != nil {
		t.Fatal(err)
	}
	if err = authorization.AssignUser(ctx, viewerUser.ID, viewerRole.ID, rbac.Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
	if response := templateRequest(handler, http.MethodGet, "/api/v1/tenants", nil, &noAccessSession, false); response.Code != http.StatusOK {
		t.Fatalf("list with Tenants.View: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(handler, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"Still Nope"}`), &noAccessSession, true); response.Code != http.StatusForbidden {
		t.Fatalf("Tenants.View must not permit create: %d %s", response.Code, response.Body.String())
	}

	// Create requires CSRF.
	if response := templateRequest(handler, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"Acme Corp"}`), &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("create without CSRF: %d %s", response.Code, response.Body.String())
	}
	created := templateRequest(handler, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"Acme Corp"}`), &admin, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Tenant tenants.Tenant `json:"tenant"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	tenantID := createdBody.Tenant.ID
	if createdBody.Tenant.Slug != "acme-corp" {
		t.Fatalf("derived slug = %q", createdBody.Tenant.Slug)
	}

	// Duplicate name is rejected.
	if response := templateRequest(handler, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"Acme Corp"}`), &admin, true); response.Code != http.StatusConflict {
		t.Fatalf("duplicate name: %d %s", response.Code, response.Body.String())
	}

	// Get.
	get := templateRequest(handler, http.MethodGet, "/api/v1/tenants/"+tenantID, nil, &admin, false)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "Acme Corp") {
		t.Fatalf("get: %d %s", get.Code, get.Body.String())
	}
	if response := templateRequest(handler, http.MethodGet, "/api/v1/tenants/does-not-exist", nil, &admin, false); response.Code != http.StatusNotFound {
		t.Fatalf("get unknown: %d %s", response.Code, response.Body.String())
	}

	// Patch requires CSRF and Tenants.Manage; tenant ID stays immutable.
	if response := templateRequest(handler, http.MethodPatch, "/api/v1/tenants/"+tenantID, []byte(`{"name":"Acme Corporation"}`), &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("patch without CSRF: %d %s", response.Code, response.Body.String())
	}
	patched := templateRequest(handler, http.MethodPatch, "/api/v1/tenants/"+tenantID, []byte(`{"name":"Acme Corporation"}`), &admin, true)
	if patched.Code != http.StatusOK || !strings.Contains(patched.Body.String(), "Acme Corporation") || !strings.Contains(patched.Body.String(), tenantID) {
		t.Fatalf("patch: %d %s", patched.Code, patched.Body.String())
	}

	// Members: add/list/remove.
	memberUser, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "acme-member", Email: "acme-member@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	if response := templateRequest(handler, http.MethodPost, "/api/v1/tenants/"+tenantID+"/members", []byte(`{"user_id":"`+memberUser.ID+`"}`), &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("add member without CSRF: %d %s", response.Code, response.Body.String())
	}
	addMember := templateRequest(handler, http.MethodPost, "/api/v1/tenants/"+tenantID+"/members", []byte(`{"user_id":"`+memberUser.ID+`"}`), &admin, true)
	if addMember.Code != http.StatusCreated {
		t.Fatalf("add member: %d %s", addMember.Code, addMember.Body.String())
	}
	if response := templateRequest(handler, http.MethodPost, "/api/v1/tenants/"+tenantID+"/members", []byte(`{"user_id":"`+memberUser.ID+`"}`), &admin, true); response.Code != http.StatusConflict {
		t.Fatalf("duplicate member: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(handler, http.MethodPost, "/api/v1/tenants/"+tenantID+"/members", []byte(`{"user_id":"does-not-exist"}`), &admin, true); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown member user: %d %s", response.Code, response.Body.String())
	}
	listMembers := templateRequest(handler, http.MethodGet, "/api/v1/tenants/"+tenantID+"/members", nil, &admin, false)
	if listMembers.Code != http.StatusOK || !strings.Contains(listMembers.Body.String(), memberUser.Username) {
		t.Fatalf("list members: %d %s", listMembers.Code, listMembers.Body.String())
	}
	// Membership alone (no role assignment) must never appear as a
	// Tenants.View/Manage capability; verified end to end in
	// cross_tenant_test.go's membership-only provisioning case.
	if response := templateRequest(handler, http.MethodDelete, "/api/v1/tenants/"+tenantID+"/members/"+memberUser.ID, nil, &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("remove member without CSRF: %d %s", response.Code, response.Body.String())
	}
	removeMember := templateRequest(handler, http.MethodDelete, "/api/v1/tenants/"+tenantID+"/members/"+memberUser.ID, nil, &admin, true)
	if removeMember.Code != http.StatusNoContent {
		t.Fatalf("remove member: %d %s", removeMember.Code, removeMember.Body.String())
	}
	if response := templateRequest(handler, http.MethodDelete, "/api/v1/tenants/"+tenantID+"/members/"+memberUser.ID, nil, &admin, true); response.Code != http.StatusNotFound {
		t.Fatalf("remove already-removed member: %d %s", response.Code, response.Body.String())
	}

	// Access: reuses existing RBAC assignment infrastructure. Gated by
	// global Roles.View, like GET /servers/{id}/access.
	tenantRole, err := authorization.CreateRole(ctx, "Tenant Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, tenantRole.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	assignBody, _ := json.Marshal(map[string]any{"role_id": tenantRole.ID, "scope_type": "tenant", "scope_id": tenantID})
	assign := templateRequest(handler, http.MethodPost, "/api/v1/users/"+memberUser.ID+"/roles", assignBody, &admin, true)
	if assign.Code != http.StatusCreated {
		t.Fatalf("assign tenant role via existing endpoint: %d %s", assign.Code, assign.Body.String())
	}
	access := templateRequest(handler, http.MethodGet, "/api/v1/tenants/"+tenantID+"/access", nil, &admin, false)
	if access.Code != http.StatusOK || !strings.Contains(access.Body.String(), memberUser.Username) || !strings.Contains(access.Body.String(), tenantRole.ID) {
		t.Fatalf("tenant access listing: %d %s", access.Code, access.Body.String())
	}
	// A user with only Tenants.View (not Roles.View) cannot read access.
	if response := templateRequest(handler, http.MethodGet, "/api/v1/tenants/"+tenantID+"/access", nil, &noAccessSession, false); response.Code != http.StatusForbidden {
		t.Fatalf("access without Roles.View: %d %s", response.Code, response.Body.String())
	}

	// Servers listing for a tenant with none yet.
	servers := templateRequest(handler, http.MethodGet, "/api/v1/tenants/"+tenantID+"/servers", nil, &admin, false)
	if servers.Code != http.StatusOK || !strings.Contains(servers.Body.String(), `"servers":[]`) {
		t.Fatalf("tenant servers (empty): %d %s", servers.Code, servers.Body.String())
	}

	// Delete requires CSRF; succeeds for an empty tenant.
	if response := templateRequest(handler, http.MethodDelete, "/api/v1/tenants/"+tenantID, nil, &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF: %d %s", response.Code, response.Body.String())
	}
	deleted := templateRequest(handler, http.MethodDelete, "/api/v1/tenants/"+tenantID, nil, &admin, true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if response := templateRequest(handler, http.MethodGet, "/api/v1/tenants/"+tenantID, nil, &admin, false); response.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d %s", response.Code, response.Body.String())
	}
}

func TestTenantDeleteRejectedWithServersAndAuditRecorded(t *testing.T) {
	handler, db := newTestServer(t)
	admin := createAdminSession(t, handler)

	created := templateRequest(handler, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"Has Servers"}`), &admin, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Tenant tenants.Tenant `json:"tenant"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	tenantID := createdBody.Tenant.ID

	server := createServerForTenant(t, handler, admin, tenantID, "Tenant Server")
	if response := templateRequest(handler, http.MethodDelete, "/api/v1/tenants/"+tenantID, nil, &admin, true); response.Code != http.StatusConflict {
		t.Fatalf("delete tenant with server: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(handler, http.MethodDelete, "/api/v1/servers/"+server, nil, &admin, true); response.Code != http.StatusNoContent {
		t.Fatalf("remove server: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(handler, http.MethodDelete, "/api/v1/tenants/"+tenantID, nil, &admin, true); response.Code != http.StatusNoContent {
		t.Fatalf("delete tenant after removing server: %d %s", response.Code, response.Body.String())
	}

	var creates, failedDeletes, successDeletes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='tenant.create'`).Scan(&creates); err != nil || creates < 1 {
		t.Fatalf("tenant.create audit=%d err=%v", creates, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='tenant.delete' AND result='failure'`).Scan(&failedDeletes); err != nil || failedDeletes < 1 {
		t.Fatalf("failed tenant.delete audit=%d err=%v", failedDeletes, err)
	}
	// GET /audit?resource_type=tenant must be accepted (not rejected as an
	// "invalid resource type") and must return only tenant-resource events -
	// found missing during Step 5 real-browser acceptance testing, where the
	// Tenant admin UI's own audit trail was unreachable through this filter.
	if response := templateRequest(handler, http.MethodGet, "/api/v1/audit?resource_type=tenant", nil, &admin, true); response.Code != http.StatusOK {
		t.Fatalf("audit resource_type=tenant: %d %s", response.Code, response.Body.String())
	} else {
		var body struct {
			Items []struct {
				ResourceType string `json:"resource_type"`
			} `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) == 0 {
			t.Fatal("expected at least one tenant audit event")
		}
		for _, item := range body.Items {
			if item.ResourceType != "tenant" {
				t.Fatalf("resource_type filter leaked a %q event", item.ResourceType)
			}
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='tenant.delete' AND result='success'`).Scan(&successDeletes); err != nil || successDeletes < 1 {
		t.Fatalf("successful tenant.delete audit=%d err=%v", successDeletes, err)
	}
}

// createServerForTenant registers a server for tenantID through the ordinary
// global Server.Create path (POST /servers), matching how an administrator
// assigns a server's tenant today. It returns the new server's ID.
func createServerForTenant(t *testing.T, handler http.Handler, admin testSession, tenantID, name string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"tenant_id":             tenantID,
		"creation_mode":         "custom",
		"name":                  name,
		"working_directory":     filepath.ToSlash(filepath.Dir(executable)),
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
		t.Fatalf("create server for tenant: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		Server struct {
			ID       string `json:"id"`
			TenantID string `json:"tenant_id"`
		} `json:"server"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Server.TenantID != tenantID {
		t.Fatalf("created server tenant_id = %q, want %q", created.Server.TenantID, tenantID)
	}
	return created.Server.ID
}
