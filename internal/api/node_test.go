package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
)

// fakeRemoteClient is a controllable stand-in for internal/remote.Client so
// controller-facing API tests never make a real network call.
type fakeRemoteClient struct {
	enrollResult remote.EnrollResult
	enrollErr    error
	infoResult   remote.NodeInfo
	infoErr      error

	startJob    provisioning.Job
	startErr    error
	getJob      provisioning.Job
	getErr      error
	cancelJob   provisioning.Job
	cancelErr   error
	lastStart   remote.ProvisioningRequest
	startCalls  int
	getCalls    int
	cancelCalls int
}

func (f *fakeRemoteClient) Enroll(ctx context.Context, endpoint, pairingToken string) (remote.EnrollResult, error) {
	return f.enrollResult, f.enrollErr
}
func (f *fakeRemoteClient) GetNodeInfo(ctx context.Context, endpoint, credential string) (remote.NodeInfo, error) {
	return f.infoResult, f.infoErr
}
func (f *fakeRemoteClient) GetHealth(ctx context.Context, endpoint, credential string) (remote.HealthResult, error) {
	return remote.HealthResult{Status: "healthy"}, f.infoErr
}
func (f *fakeRemoteClient) StartProvisioning(ctx context.Context, endpoint, credential string, req remote.ProvisioningRequest) (provisioning.Job, error) {
	f.startCalls++
	f.lastStart = req
	return f.startJob, f.startErr
}
func (f *fakeRemoteClient) GetProvisioningJob(ctx context.Context, endpoint, credential, jobID string) (provisioning.Job, error) {
	f.getCalls++
	return f.getJob, f.getErr
}
func (f *fakeRemoteClient) CancelProvisioningJob(ctx context.Context, endpoint, credential, jobID string) (provisioning.Job, error) {
	f.cancelCalls++
	return f.cancelJob, f.cancelErr
}

func newNodeTestServer(t *testing.T, fake *fakeRemoteClient) (http.Handler, *sql.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	handler := api.New(auth.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative()), slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), false, api.Options{RemoteClient: fake}).Handler(http.NotFoundHandler())
	return handler, db
}

func grantNodePermission(t *testing.T, db *sql.DB, userID, permission string) {
	t.Helper()
	ctx := context.Background()
	authorization := rbac.New(db)
	role, err := authorization.CreateRole(ctx, permission+"-role-"+userID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := authorization.ReplacePermissions(ctx, role.ID, []string{permission}); err != nil {
		t.Fatal(err)
	}
	if err := authorization.AssignUser(ctx, userID, role.ID, rbac.Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
}

func TestNodeInfoRequiresMachineAuth(t *testing.T) {
	h, _ := newNodeTestServer(t, &fakeRemoteClient{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/node/info", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without machine credential, got %d: %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/node/info", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-credential")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with unknown credential, got %d: %s", w.Code, w.Body.String())
	}
}

func TestNodeInfoUnauthenticatedBrowserSessionCannotAuthenticate(t *testing.T) {
	// A browser cookie session must not double as a machine credential (see
	// AGENTS.md item 13): calling the Node-facing API with only a cookie,
	// no Authorization: Bearer header, must fail.
	h, _ := newNodeTestServer(t, &fakeRemoteClient{})
	admin := createAdminSession(t, h)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/node/info", nil)
	req.AddCookie(admin.cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected browser session alone to be rejected, got %d: %s", w.Code, w.Body.String())
	}
}

// TestPairingAndEnrollmentEndToEnd walks the full flow: an operator with
// Node.Manage generates a pairing token on "this" node, and a simulated
// controller call to /api/v1/node/enroll consumes it and receives a machine
// credential that then authenticates the Node-facing API.
func TestPairingAndEnrollmentEndToEnd(t *testing.T) {
	h, _ := newNodeTestServer(t, &fakeRemoteClient{})
	admin := createAdminSession(t, h)

	pairing := templateRequest(h, http.MethodPost, "/api/v1/node/pairing-tokens", nil, &admin, true)
	if pairing.Code != http.StatusOK {
		t.Fatalf("create pairing token: %d %s", pairing.Code, pairing.Body.String())
	}
	var pairingBody struct {
		PairingToken string `json:"pairing_token"`
	}
	if err := json.Unmarshal(pairing.Body.Bytes(), &pairingBody); err != nil {
		t.Fatal(err)
	}
	if pairingBody.PairingToken == "" {
		t.Fatal("expected a non-empty pairing token")
	}

	enroll := httptest.NewRequest(http.MethodPost, "/api/v1/node/enroll", bytes.NewBufferString(`{"pairing_token":"`+pairingBody.PairingToken+`"}`))
	enroll.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, enroll)
	if w.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	var enrollBody struct {
		Credential      string `json:"credential"`
		NodeID          string `json:"node_id"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &enrollBody); err != nil {
		t.Fatal(err)
	}
	if enrollBody.Credential == "" || enrollBody.NodeID == "" || enrollBody.ProtocolVersion == 0 {
		t.Fatalf("unexpected enroll response: %s", w.Body.String())
	}

	// Replaying the same pairing token must fail.
	replay := httptest.NewRequest(http.MethodPost, "/api/v1/node/enroll", bytes.NewBufferString(`{"pairing_token":"`+pairingBody.PairingToken+`"}`))
	replay.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, replay)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected replayed pairing token to be rejected, got %d", w.Code)
	}

	// The issued credential now authenticates the Node-facing API.
	info := httptest.NewRequest(http.MethodGet, "/api/v1/node/info", nil)
	info.Header.Set("Authorization", "Bearer "+enrollBody.Credential)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, info)
	if w.Code != http.StatusOK {
		t.Fatalf("node info with issued credential: %d %s", w.Code, w.Body.String())
	}
}

func TestNodePairingTokenRequiresManagePermissionAndCSRF(t *testing.T) {
	h, db := newNodeTestServer(t, &fakeRemoteClient{})
	admin := createAdminSession(t, h)
	identities := identity.New(db)
	viewer, err := identities.CreateUser(context.Background(), identity.CreateUserInput{Username: "no-node-access", Email: "no-node-access@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	viewerSession := loginSession(t, h, viewer.Username)
	if response := templateRequest(h, http.MethodPost, "/api/v1/node/pairing-tokens", nil, &viewerSession, true); response.Code != http.StatusForbidden {
		t.Fatalf("pairing token without Node.Manage: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(h, http.MethodPost, "/api/v1/node/pairing-tokens", nil, &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("pairing token without CSRF: %d %s", response.Code, response.Body.String())
	}
}

func TestRemoteNodesRBACAndCSRF(t *testing.T) {
	h, db := newNodeTestServer(t, &fakeRemoteClient{
		enrollResult: remote.EnrollResult{NodeID: "remote-1", DisplayName: "Remote One", Credential: "issued-credential", ProtocolVersion: 1, GameNodeVersion: "0.5.0", OS: "linux", Arch: "amd64", Capabilities: []string{"console"}},
	})
	admin := createAdminSession(t, h)
	identities := identity.New(db)
	viewerUser, err := identities.CreateUser(context.Background(), identity.CreateUserInput{Username: "node-viewer", Email: "node-viewer@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	grantNodePermission(t, db, viewerUser.ID, "Node.View")
	viewerSession := loginSession(t, h, viewerUser.Username)

	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes", nil, &viewerSession, false); response.Code != http.StatusOK {
		t.Fatalf("list with Node.View: %d %s", response.Code, response.Body.String())
	}
	enrollBody := []byte(`{"endpoint":"https://remote-one.internal:8443","pairing_token":"whatever","display_name":"Remote One"}`)
	if response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes", enrollBody, &viewerSession, true); response.Code != http.StatusForbidden {
		t.Fatalf("enroll with only Node.View: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes", enrollBody, &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("enroll without CSRF: %d %s", response.Code, response.Body.String())
	}
	created := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes", enrollBody, &admin, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("enroll: %d %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		RemoteNode struct {
			ID            string `json:"id"`
			Compatibility string `json:"compatibility"`
		} `json:"remote_node"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	if createdBody.RemoteNode.ID == "" || createdBody.RemoteNode.Compatibility != "compatible" {
		t.Fatalf("unexpected enroll response: %s", created.Body.String())
	}
	if strings.Contains(created.Body.String(), "issued-credential") {
		t.Fatal("controller-facing API must never return the stored machine credential")
	}

	if response := templateRequest(h, http.MethodDelete, "/api/v1/remote-nodes/"+createdBody.RemoteNode.ID, nil, &viewerSession, true); response.Code != http.StatusForbidden {
		t.Fatalf("delete with only Node.View: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(h, http.MethodDelete, "/api/v1/remote-nodes/"+createdBody.RemoteNode.ID, nil, &admin, true); response.Code != http.StatusNoContent {
		t.Fatalf("delete as admin: %d %s", response.Code, response.Body.String())
	}
}

func TestRemoteNodeEnrollmentRejectsInvalidEndpoint(t *testing.T) {
	h, _ := newNodeTestServer(t, &fakeRemoteClient{})
	admin := createAdminSession(t, h)
	body := []byte(`{"endpoint":"not-a-valid-endpoint","pairing_token":"x"}`)
	response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes", body, &admin, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid endpoint to be rejected, got %d: %s", response.Code, response.Body.String())
	}
}

func TestRemoteNodeEnrollmentSurfacesRemoteAuthFailure(t *testing.T) {
	h, _ := newNodeTestServer(t, &fakeRemoteClient{enrollErr: &remote.Error{Kind: remote.KindAuthenticationFailed, Detail: "status 401"}})
	admin := createAdminSession(t, h)
	body := []byte(`{"endpoint":"https://remote.internal","pairing_token":"bad-token"}`)
	response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes", body, &admin, true)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for remote authentication failure, got %d: %s", response.Code, response.Body.String())
	}
	var body2 struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body2); err != nil {
		t.Fatal(err)
	}
	if body2.Error.Code != string(remote.KindAuthenticationFailed) {
		t.Fatalf("expected controlled error code, got %q", body2.Error.Code)
	}
}
