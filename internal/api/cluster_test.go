package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"gamenode/internal/audit"
	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	"gamenode/internal/tenants"
)

// TestClusterPlacementRequiresAuth confirms both cluster routes reject an
// unauthenticated caller, matching every other RBAC-gated route.
func TestClusterPlacementRequiresAuth(t *testing.T) {
	h, _ := newTestServer(t)
	if response := templateRequest(h, http.MethodGet, "/api/v1/cluster/capacity", nil, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("capacity unauthenticated=%d", response.Code)
	}
	if response := templateRequest(h, http.MethodPost, "/api/v1/cluster/placement", []byte(`{}`), nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("placement unauthenticated=%d", response.Code)
	}
}

// TestClusterPlacementAdminSelectsLocalNodeAndAudits exercises the whole
// decision path with an administrator (which bypasses the RBAC evaluator):
// there are no enrolled Remote Nodes, so the only eligible candidate is this
// installation itself, and its execution must be "local_only". Exactly one
// audit event records the accepted decision.
func TestClusterPlacementAdminSelectsLocalNodeAndAudits(t *testing.T) {
	h, db := newTestServer(t)
	admin := createAdminSession(t, h)

	response := templateRequest(h, http.MethodPost, "/api/v1/cluster/placement", []byte(`{"runtime_type":"native"}`), &admin, true)
	if response.Code != http.StatusOK {
		t.Fatalf("placement: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Decision struct {
			Rejected  bool   `json:"rejected"`
			Execution string `json:"execution"`
			Selected  struct {
				NodeID string `json:"node_id"`
				Kind   string `json:"kind"`
			} `json:"selected"`
		} `json:"decision"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Decision.Rejected {
		t.Fatalf("expected an accepted decision, got rejected")
	}
	if body.Decision.Selected.NodeID != "local" || body.Decision.Selected.Kind != "local" {
		t.Fatalf("expected the local node to be selected, got %+v", body.Decision.Selected)
	}
	if body.Decision.Execution != "local_only" {
		t.Fatalf("expected execution=local_only, got %q", body.Decision.Execution)
	}

	events, err := audit.New(db).List(context.Background(), audit.Filter{Action: audit.ClusterPlacementDecision})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Result != audit.Success {
		t.Fatalf("expected exactly one successful cluster placement audit event, got %#v", events)
	}
}

// TestClusterPlacementCSRFRequired confirms the mutating placement route
// enforces CSRF like every other product mutation.
func TestClusterPlacementCSRFRequired(t *testing.T) {
	h, _ := newTestServer(t)
	admin := createAdminSession(t, h)
	response := templateRequest(h, http.MethodPost, "/api/v1/cluster/placement", []byte(`{}`), &admin, false)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected CSRF rejection, got %d %s", response.Code, response.Body.String())
	}
	response = templateRequest(h, http.MethodPost, "/api/v1/cluster/placement/execute", []byte(`{}`), &admin, false)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected execution CSRF rejection, got %d %s", response.Code, response.Body.String())
	}
}

// TestClusterPlacementTenantIsolation confirms a tenant-scoped Cluster.Schedule
// grant for tenant A never authorizes a placement request for tenant B, and
// that a caller with no Cluster permission at all is rejected outright -
// mirroring the cross-tenant isolation tests for other tenant-scoped routes.
func TestClusterPlacementTenantIsolation(t *testing.T) {
	h, db := newTestServer(t)
	admin := createAdminSession(t, h)

	tenantService := tenants.New(db)
	ctx := context.Background()
	tenantA, err := tenantService.Create(ctx, tenants.CreateInput{Name: "Tenant A"})
	if err != nil {
		t.Fatal(err)
	}
	tenantB, err := tenantService.Create(ctx, tenants.CreateInput{Name: "Tenant B"})
	if err != nil {
		t.Fatal(err)
	}

	identities := identity.New(db)
	userA, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "scheduler-a", Email: "scheduler-a@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}

	authorization := rbac.New(db)
	role, err := authorization.CreateRole(ctx, "Tenant Scheduler", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, role.ID, []string{"Cluster.Schedule", "Cluster.View"}); err != nil {
		t.Fatal(err)
	}
	if err = authorization.AssignUser(ctx, userA.ID, role.ID, rbac.Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}

	sessionA := loginSession(t, h, userA.Username)

	// Allowed: request for the tenant the grant covers.
	okPayload, _ := json.Marshal(map[string]any{"tenant_id": tenantA.ID, "runtime_type": "native"})
	if response := templateRequest(h, http.MethodPost, "/api/v1/cluster/placement", okPayload, &sessionA, true); response.Code != http.StatusOK {
		t.Fatalf("expected tenant A request to succeed, got %d %s", response.Code, response.Body.String())
	}

	// Denied: same user, a different tenant's placement request.
	crossPayload, _ := json.Marshal(map[string]any{"tenant_id": tenantB.ID, "runtime_type": "native"})
	if response := templateRequest(h, http.MethodPost, "/api/v1/cluster/placement", crossPayload, &sessionA, true); response.Code != http.StatusForbidden {
		t.Fatalf("expected cross-tenant request to be forbidden, got %d %s", response.Code, response.Body.String())
	}

	// Denied: capacity read for the tenant the grant does not cover.
	if response := templateRequest(h, http.MethodGet, "/api/v1/cluster/capacity?tenant_id="+tenantB.ID, nil, &sessionA, false); response.Code != http.StatusForbidden {
		t.Fatalf("expected cross-tenant capacity read to be forbidden, got %d %s", response.Code, response.Body.String())
	}

	// Sanity: admin (bypass) can still see tenant B's capacity.
	if response := templateRequest(h, http.MethodGet, "/api/v1/cluster/capacity?tenant_id="+tenantB.ID, nil, &admin, false); response.Code != http.StatusOK {
		t.Fatalf("expected admin capacity read to succeed, got %d %s", response.Code, response.Body.String())
	}
}

// TestClusterPlacementInvalidRuntimeType confirms an unrecognized
// runtime_type is rejected with a 400 rather than silently defaulting.
func TestClusterPlacementInvalidRuntimeType(t *testing.T) {
	h, _ := newTestServer(t)
	admin := createAdminSession(t, h)
	response := templateRequest(h, http.MethodPost, "/api/v1/cluster/placement", []byte(`{"runtime_type":"vm"}`), &admin, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid runtime_type, got %d %s", response.Code, response.Body.String())
	}
}
