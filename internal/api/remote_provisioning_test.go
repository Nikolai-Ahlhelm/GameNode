package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/nodes"
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
)

type remoteProvisionFixture struct {
	handler http.Handler
	db      *sql.DB
	nodes   *nodes.Service
	remote  *fakeRemoteClient
}

func newRemoteProvisionFixture(t *testing.T, fake *fakeRemoteClient) remoteProvisionFixture {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	nodesService := nodes.New(db)
	handler := api.New(auth.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative()), slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{RemoteNodes: nodesService, RemoteClient: fake}).Handler(http.NotFoundHandler())
	return remoteProvisionFixture{handler: handler, db: db, nodes: nodesService, remote: fake}
}

func TestRemoteNodeProvisioningRequiresAuth(t *testing.T) {
	fixture := newRemoteProvisionFixture(t, &fakeRemoteClient{})
	node := enrollFakeRemoteNode(t, fixture.nodes, []string{"container_runtime"})
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/remote-nodes/"+node.ID+"/provisioning", executeBody("t", "s", "s", "container", "ghcr.io/example/game:1"), nil, false)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d %s", response.Code, response.Body.String())
	}
}

func TestRemoteNodeProvisioningCSRFRequired(t *testing.T) {
	fixture := newRemoteProvisionFixture(t, &fakeRemoteClient{})
	node := enrollFakeRemoteNode(t, fixture.nodes, []string{"container_runtime"})
	admin := createAdminSession(t, fixture.handler)
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/remote-nodes/"+node.ID+"/provisioning", executeBody("t", "s", "s", "container", "ghcr.io/example/game:1"), &admin, false)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected CSRF rejection, got %d %s", response.Code, response.Body.String())
	}
}

// TestRemoteNodeProvisioningForwardsThroughNodeAPI confirms the controller
// proxy forwards the typed request through internal/remote.Client -
// StartProvisioning - to the machine-authenticated Node API, and returns the
// job the target node reports, unmodified.
func TestRemoteNodeProvisioningForwardsThroughNodeAPI(t *testing.T) {
	fake := &fakeRemoteClient{startJob: provisioning.Job{ID: "job-1", RuntimeType: "container", TenantID: "default", Status: provisioning.Pending}}
	fixture := newRemoteProvisionFixture(t, fake)
	node := enrollFakeRemoteNode(t, fixture.nodes, []string{"container_runtime"})
	admin := createAdminSession(t, fixture.handler)

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/remote-nodes/"+node.ID+"/provisioning", executeBody("template-1", "Remote Server", "remote-server", "container", "ghcr.io/example/game:1"), &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	var job provisioning.Job
	json.NewDecoder(response.Body).Decode(&job)
	if job.ID != "job-1" {
		t.Fatalf("expected the remote job to be returned unmodified, got %+v", job)
	}
	if fake.startCalls != 1 || fake.lastStart.TemplateID != "template-1" || fake.lastStart.Image != "ghcr.io/example/game:1" {
		t.Fatalf("unexpected forwarded request: %+v", fake.lastStart)
	}
}

// TestRemoteNodeProvisioningNodeOffline confirms an unreachable node
// surfaces as a bad-gateway error, never a silent local fallback.
func TestRemoteNodeProvisioningNodeOffline(t *testing.T) {
	fake := &fakeRemoteClient{startErr: &remote.Error{Kind: remote.KindUnreachable, Detail: "connection failed"}}
	fixture := newRemoteProvisionFixture(t, fake)
	node := enrollFakeRemoteNode(t, fixture.nodes, []string{"container_runtime"})
	admin := createAdminSession(t, fixture.handler)

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/remote-nodes/"+node.ID+"/provisioning", executeBody("template-1", "s", "s", "container", "ghcr.io/example/game:1"), &admin, true)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d %s", response.Code, response.Body.String())
	}
}

// TestRemoteNodeProvisioningDisabledNodeRejected confirms a disabled Remote
// Node registry entry is never dispatched to, even with a valid stored
// credential.
func TestRemoteNodeProvisioningDisabledNodeRejected(t *testing.T) {
	fake := &fakeRemoteClient{}
	fixture := newRemoteProvisionFixture(t, fake)
	node := enrollFakeRemoteNode(t, fixture.nodes, []string{"container_runtime"})
	if _, err := fixture.nodes.SetEnabled(context.Background(), node.ID, false); err != nil {
		t.Fatal(err)
	}
	admin := createAdminSession(t, fixture.handler)

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/remote-nodes/"+node.ID+"/provisioning", executeBody("template-1", "s", "s", "container", "ghcr.io/example/game:1"), &admin, true)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for a disabled node, got %d %s", response.Code, response.Body.String())
	}
	if fake.startCalls != 0 {
		t.Fatalf("expected no dispatch to a disabled node, got %d calls", fake.startCalls)
	}
}

// TestRemoteNodeProvisioningJobStatusAndCancelForwarded confirms GET status
// and POST cancel both forward through the Node API and enforce the same
// tenant check the create route does.
func TestRemoteNodeProvisioningJobStatusAndCancelForwarded(t *testing.T) {
	fake := &fakeRemoteClient{
		getJob:    provisioning.Job{ID: "job-1", TenantID: "default", Status: provisioning.Installing},
		cancelJob: provisioning.Job{ID: "job-1", TenantID: "default", Status: provisioning.Cancelled},
	}
	fixture := newRemoteProvisionFixture(t, fake)
	node := enrollFakeRemoteNode(t, fixture.nodes, []string{"container_runtime"})
	admin := createAdminSession(t, fixture.handler)

	statusResponse := templateRequest(fixture.handler, http.MethodGet, "/api/v1/remote-nodes/"+node.ID+"/provisioning/job-1", nil, &admin, false)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status: %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	if fake.getCalls != 1 {
		t.Fatalf("expected exactly one GetProvisioningJob call, got %d", fake.getCalls)
	}

	cancelResponse := templateRequest(fixture.handler, http.MethodPost, "/api/v1/remote-nodes/"+node.ID+"/provisioning/job-1/cancel", nil, &admin, true)
	if cancelResponse.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", cancelResponse.Code, cancelResponse.Body.String())
	}
	if fake.cancelCalls != 1 {
		t.Fatalf("expected exactly one CancelProvisioningJob call, got %d", fake.cancelCalls)
	}
}

// TestRemoteNodeProvisioningJobTenantMismatchNotFound confirms a caller who
// declares a tenant the returned job does not actually belong to is refused
// - the controller trusts only the target node's own tenant field, never
// the caller's claim alone, and keeps no cluster-wide job/tenant mapping of
// its own.
func TestRemoteNodeProvisioningJobTenantMismatchNotFound(t *testing.T) {
	fake := &fakeRemoteClient{getJob: provisioning.Job{ID: "job-1", TenantID: "someone-elses-tenant", Status: provisioning.Installing}}
	fixture := newRemoteProvisionFixture(t, fake)
	node := enrollFakeRemoteNode(t, fixture.nodes, []string{"container_runtime"})
	admin := createAdminSession(t, fixture.handler)

	response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/remote-nodes/"+node.ID+"/provisioning/job-1?tenant_id=default", nil, &admin, false)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a tenant mismatch, got %d %s", response.Code, response.Body.String())
	}
}

// TestRemoteNodeProvisioningTenantIsolation mirrors the local provisioning
// route's tenant scoping: a Cluster/Server.Create grant scoped to tenant A
// never authorizes a request naming tenant B.
func TestRemoteNodeProvisioningTenantIsolation(t *testing.T) {
	fake := &fakeRemoteClient{startJob: provisioning.Job{ID: "job-1", TenantID: "tenant-a"}}
	fixture := newRemoteProvisionFixture(t, fake)
	node := enrollFakeRemoteNode(t, fixture.nodes, []string{"container_runtime"})
	createAdminSession(t, fixture.handler)
	tenantA := createTestTenant(t, fixture.db, "Remote Tenant A")
	tenantB := createTestTenant(t, fixture.db, "Remote Tenant B")
	userID := createUserWithPermissions(t, fixture.db, "remote-scoped", []string{"Templates.View"}, rbac.Scope{Type: "global"})
	grantPermissions(t, fixture.db, userID, "remote-scoped-role-2", []string{"Server.Create"}, rbac.Scope{Type: "tenant", ID: &tenantA})
	session := loginSession(t, fixture.handler, "remote-scoped")

	okBody, _ := json.Marshal(map[string]any{"tenant_id": tenantA, "template_id": "t", "server_name": "a", "directory_name": "a", "runtime_type": "container", "image": "ghcr.io/example/game:1"})
	if response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/remote-nodes/"+node.ID+"/provisioning", okBody, &session, true); response.Code != http.StatusAccepted {
		t.Fatalf("expected tenant A request to succeed, got %d %s", response.Code, response.Body.String())
	}
	crossBody, _ := json.Marshal(map[string]any{"tenant_id": tenantB, "template_id": "t", "server_name": "b", "directory_name": "b", "runtime_type": "container", "image": "ghcr.io/example/game:1"})
	if response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/remote-nodes/"+node.ID+"/provisioning", crossBody, &session, true); response.Code != http.StatusForbidden {
		t.Fatalf("expected cross-tenant request to be forbidden, got %d %s", response.Code, response.Body.String())
	}
}
