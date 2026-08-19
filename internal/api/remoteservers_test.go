package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
	"gamenode/internal/tenants"
)

// enrollRemoteServerTestNode drives the real HTTP enrollment flow (as an admin)
// against the given fake, returning the created registry row's id. This
// keeps these tests exercising the same enroll path as production instead
// of poking the nodes package directly.
func enrollRemoteServerTestNode(t *testing.T, h http.Handler, admin testSession, endpoint, displayName string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"endpoint": endpoint, "pairing_token": "whatever", "display_name": displayName})
	response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes", body, &admin, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("enroll %s: %d %s", displayName, response.Code, response.Body.String())
	}
	var out struct {
		RemoteNode struct {
			ID string `json:"id"`
		} `json:"remote_node"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.RemoteNode.ID
}

func sampleServerSummary(id, tenantID, name string) remote.ServerSummary {
	now := time.Now().UTC()
	return remote.ServerSummary{
		ID: id, TenantID: tenantID, Name: name, CreationMode: "custom", RuntimeType: "native",
		CreatedAt: now, UpdatedAt: now, Runtime: remote.RuntimeState{CurrentState: "stopped"},
	}
}

func remoteServerCapableFake() *fakeRemoteClient {
	return &fakeRemoteClient{
		enrollResult: remote.EnrollResult{
			NodeID: "remote-node-1", DisplayName: "Remote One", Credential: "issued-credential",
			ProtocolVersion: 1, GameNodeVersion: "0.6.0", OS: "linux", Arch: "amd64",
			Capabilities: []string{"remote_server_management", "remote_console", "remote_files", "remote_monitoring"},
		},
	}
}

// TestRemoteServersRequiresAuthAndCapability confirms the list route rejects
// an unauthenticated caller and a node that never advertised
// remote_server_management (old/incompatible node) with a controlled,
// non-crashing error instead of attempting the call.
func TestRemoteServersRequiresAuthAndCapability(t *testing.T) {
	fake := remoteServerCapableFake()
	h, _ := newNodeTestServer(t, fake)
	admin := createAdminSession(t, h)
	nodeID := enrollRemoteServerTestNode(t, h, admin, "https://remote-one.internal", "Remote One")

	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers", nil, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: %d", response.Code)
	}

	// A node enrolled without the capability must be rejected with a
	// controlled "unsupported" response, never attempted.
	oldFake := &fakeRemoteClient{enrollResult: remote.EnrollResult{
		NodeID: "old-node", DisplayName: "Old Node", Credential: "cred", ProtocolVersion: 1,
		GameNodeVersion: "0.5.0", OS: "linux", Arch: "amd64", Capabilities: []string{"console"},
	}}
	oldH, _ := newNodeTestServer(t, oldFake)
	oldAdmin := createAdminSession(t, oldH)
	oldNodeID := enrollRemoteServerTestNode(t, oldH, oldAdmin, "https://old.internal", "Old Node")
	if response := templateRequest(oldH, http.MethodGet, "/api/v1/remote-nodes/"+oldNodeID+"/servers", nil, &oldAdmin, false); response.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 for missing capability, got %d %s", response.Code, response.Body.String())
	}
}

// TestRemoteServersRBACAndCSRF confirms RemoteServer.View/Manage are
// checked independently, and every mutation requires CSRF, matching the
// existing local Server.* conventions.
func TestRemoteServersRBACAndCSRF(t *testing.T) {
	fake := remoteServerCapableFake()
	fake.serversByID = map[string]remote.ServerSummary{"srv-1": sampleServerSummary("srv-1", "default", "Alpha")}
	fake.createResult = sampleServerSummary("srv-2", "default", "Beta")
	fake.lifecycleResult = sampleServerSummary("srv-1", "default", "Alpha")
	h, db := newNodeTestServer(t, fake)
	admin := createAdminSession(t, h)
	nodeID := enrollRemoteServerTestNode(t, h, admin, "https://remote-one.internal", "Remote One")

	identities := identity.New(db)
	viewer, err := identities.CreateUser(context.Background(), identity.CreateUserInput{Username: "rs-viewer", Email: "rs-viewer@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	grantNodePermission(t, db, viewer.ID, "RemoteServer.View")
	viewerSession := loginSession(t, h, viewer.Username)

	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers", nil, &viewerSession, false); response.Code != http.StatusOK {
		t.Fatalf("list with RemoteServer.View: %d %s", response.Code, response.Body.String())
	}
	createBody, _ := json.Marshal(map[string]string{"name": "Beta", "working_directory": "C:/servers/beta", "executable": "run.exe"})
	if response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes/"+nodeID+"/servers", createBody, &viewerSession, true); response.Code != http.StatusForbidden {
		t.Fatalf("create with only RemoteServer.View: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-1/start", nil, &viewerSession, true); response.Code != http.StatusForbidden {
		t.Fatalf("start with only RemoteServer.View: %d %s", response.Code, response.Body.String())
	}

	grantNodePermission(t, db, viewer.ID, "RemoteServer.Manage")
	managerSession := loginSession(t, h, viewer.Username)
	if response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes/"+nodeID+"/servers", createBody, &managerSession, false); response.Code != http.StatusForbidden {
		t.Fatalf("create without CSRF: %d %s", response.Code, response.Body.String())
	}
	created := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes/"+nodeID+"/servers", createBody, &managerSession, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}

	start := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-1/start", nil, &managerSession, true)
	if start.Code != http.StatusOK {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}

	// Every remote-server mutation and lifecycle action is audited under the
	// remote_server resource type, distinct from local server actions.
	events, err := audit.New(db).List(context.Background(), audit.Filter{ResourceType: audit.RemoteServer})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least a create and a start audit event, got %#v", events)
	}
	for _, e := range events {
		if e.ServerID != nil {
			t.Fatalf("remote server audit events must not populate the local server_id column: %#v", e)
		}
	}
}

// TestRemoteServersTenantIsolation confirms a tenant-scoped RemoteServer.View
// grant only ever sees servers the remote node itself reports as belonging
// to that tenant, and is forbidden from a single-server GET/lifecycle call
// against a server the node reports as belonging to a different tenant -
// the controller trusts the NODE's tenant_id, never a cached or
// client-supplied one.
func TestRemoteServersTenantIsolation(t *testing.T) {
	fake := remoteServerCapableFake()
	h, db := newNodeTestServer(t, fake)
	admin := createAdminSession(t, h)
	nodeID := enrollRemoteServerTestNode(t, h, admin, "https://remote-one.internal", "Remote One")

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
	fake.serversByID = map[string]remote.ServerSummary{
		"srv-a": sampleServerSummary("srv-a", tenantA.ID, "Server A"),
		"srv-b": sampleServerSummary("srv-b", tenantB.ID, "Server B"),
	}

	identities := identity.New(db)
	userA, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "tenant-a-user", Email: "tenant-a-user@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	authorization := rbac.New(db)
	role, err := authorization.CreateRole(ctx, "Tenant A Remote Viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, role.ID, []string{"RemoteServer.View"}); err != nil {
		t.Fatal(err)
	}
	if err = authorization.AssignUser(ctx, userA.ID, role.ID, rbac.Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	sessionA := loginSession(t, h, userA.Username)

	list := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers", nil, &sessionA, false)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	var listBody struct {
		RemoteServers []struct {
			Server struct {
				ID string `json:"id"`
			} `json:"server"`
		} `json:"remote_servers"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listBody); err != nil {
		t.Fatal(err)
	}
	if len(listBody.RemoteServers) != 1 || listBody.RemoteServers[0].Server.ID != "srv-a" {
		t.Fatalf("expected only tenant A's server visible, got %#v", listBody.RemoteServers)
	}

	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-a", nil, &sessionA, false); response.Code != http.StatusOK {
		t.Fatalf("get own-tenant server: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-b", nil, &sessionA, false); response.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden for another tenant's server, got %d %s", response.Code, response.Body.String())
	}
}

// TestRemoteServerNodeIsolation confirms a server id that exists on one
// enrolled node is never visible or actionable through a different node's
// registry entry - the controller always resolves the server against the
// SPECIFIC node named in the URL, never a global server-id lookup.
func TestRemoteServerNodeIsolation(t *testing.T) {
	fake := &fakeRemoteClient{
		enrollByEndpoint: map[string]remote.EnrollResult{
			"https://node-a.internal": {NodeID: "node-a", DisplayName: "Node A", Credential: "cred-a", ProtocolVersion: 1, GameNodeVersion: "0.6.0", OS: "linux", Arch: "amd64", Capabilities: []string{"remote_server_management"}},
			"https://node-b.internal": {NodeID: "node-b", DisplayName: "Node B", Credential: "cred-b", ProtocolVersion: 1, GameNodeVersion: "0.6.0", OS: "linux", Arch: "amd64", Capabilities: []string{"remote_server_management"}},
		},
		serversByEndpoint: map[string]map[string]remote.ServerSummary{
			"https://node-a.internal": {"srv-on-a": sampleServerSummary("srv-on-a", "default", "On A")},
			"https://node-b.internal": {"srv-on-b": sampleServerSummary("srv-on-b", "default", "On B")},
		},
	}
	h, _ := newNodeTestServer(t, fake)
	admin := createAdminSession(t, h)
	nodeAID := enrollRemoteServerTestNode(t, h, admin, "https://node-a.internal", "Node A")
	nodeBID := enrollRemoteServerTestNode(t, h, admin, "https://node-b.internal", "Node B")

	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeAID+"/servers/srv-on-a", nil, &admin, false); response.Code != http.StatusOK {
		t.Fatalf("expected srv-on-a via node A to succeed, got %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeBID+"/servers/srv-on-a", nil, &admin, false); response.Code != http.StatusNotFound {
		t.Fatalf("expected srv-on-a via node B to 404, got %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeAID+"/servers/srv-on-b", nil, &admin, false); response.Code != http.StatusNotFound {
		t.Fatalf("expected srv-on-b via node A to 404, got %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes/"+nodeAID+"/servers/srv-on-b/start", nil, &admin, true); response.Code != http.StatusNotFound {
		t.Fatalf("expected start srv-on-b via node A to 404, got %d %s", response.Code, response.Body.String())
	}
}

// TestRemoteServerDisabledNodeIsRejectedWithoutContact confirms a disabled
// registry entry never reaches the remote client at all.
func TestRemoteServerDisabledNodeIsRejectedWithoutContact(t *testing.T) {
	fake := remoteServerCapableFake()
	fake.serversByID = map[string]remote.ServerSummary{"srv-1": sampleServerSummary("srv-1", "default", "Alpha")}
	h, _ := newNodeTestServer(t, fake)
	admin := createAdminSession(t, h)
	nodeID := enrollRemoteServerTestNode(t, h, admin, "https://remote-one.internal", "Remote One")

	disable, _ := json.Marshal(map[string]bool{"enabled": false})
	if response := templateRequest(h, http.MethodPatch, "/api/v1/remote-nodes/"+nodeID, disable, &admin, true); response.Code != http.StatusOK {
		t.Fatalf("disable node: %d %s", response.Code, response.Body.String())
	}
	response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers", nil, &admin, false)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409 node_disabled, got %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "node_disabled" {
		t.Fatalf("expected node_disabled error code, got %q", body.Error.Code)
	}
}

// TestRemoteServerUnreachableNodeReturnsControlledError confirms a
// transport-level failure (node unreachable) never leaks a raw Go error to
// the browser - only the sanitized internal/remote.Kind.
func TestRemoteServerUnreachableNodeReturnsControlledError(t *testing.T) {
	fake := remoteServerCapableFake()
	fake.getErr = &remote.Error{Kind: remote.KindUnreachable, Detail: "connection failed"}
	h, _ := newNodeTestServer(t, fake)
	admin := createAdminSession(t, h)
	nodeID := enrollRemoteServerTestNode(t, h, admin, "https://remote-one.internal", "Remote One")

	response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-1", nil, &admin, false)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for unreachable node, got %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(remote.KindUnreachable) {
		t.Fatalf("expected node_unreachable, got %q", body.Error.Code)
	}
}

// TestRemoteConsoleSendRequiresSeparatePermission confirms
// RemoteConsole.View does not implicitly grant RemoteConsole.Send - the
// same separation the local Console.View/Console.Send permissions enforce.
func TestRemoteConsoleSendRequiresSeparatePermission(t *testing.T) {
	fake := remoteServerCapableFake()
	fake.serversByID = map[string]remote.ServerSummary{"srv-1": sampleServerSummary("srv-1", "default", "Alpha")}
	fake.consoleSnapshot = remote.ConsoleSnapshot{State: "running", Events: []remote.ConsoleEvent{{Type: "output", Data: "hello\n"}}}
	h, db := newNodeTestServer(t, fake)
	admin := createAdminSession(t, h)
	nodeID := enrollRemoteServerTestNode(t, h, admin, "https://remote-one.internal", "Remote One")

	identities := identity.New(db)
	viewer, err := identities.CreateUser(context.Background(), identity.CreateUserInput{Username: "console-viewer", Email: "console-viewer@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	grantNodePermission(t, db, viewer.ID, "RemoteConsole.View")
	session := loginSession(t, h, viewer.Username)

	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-1/console", nil, &session, false); response.Code != http.StatusOK {
		t.Fatalf("console view: %d %s", response.Code, response.Body.String())
	}
	sendBody, _ := json.Marshal(map[string]string{"data": "say hello\n"})
	if response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-1/console", sendBody, &session, true); response.Code != http.StatusForbidden {
		t.Fatalf("expected console send to be forbidden without RemoteConsole.Send, got %d %s", response.Code, response.Body.String())
	}

	grantNodePermission(t, db, viewer.ID, "RemoteConsole.Send")
	senderSession := loginSession(t, h, viewer.Username)
	if response := templateRequest(h, http.MethodPost, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-1/console", sendBody, &senderSession, true); response.Code != http.StatusNoContent {
		t.Fatalf("console send: %d %s", response.Code, response.Body.String())
	}

	// Console content itself must never be audited (only the fact that
	// input was sent, with byte count metadata).
	events, err := audit.New(db).List(context.Background(), audit.Filter{Action: audit.RemoteConsoleInput})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly one remote console input audit event, got %#v", events)
	}
	if events[0].Metadata == nil {
		t.Fatal("expected metadata")
	}
	raw := string(events[0].Metadata)
	if strings.Contains(raw, "say hello") {
		t.Fatalf("console content must never be audited: %s", raw)
	}
}

// TestRemoteFilesReadOnlyWithoutMutationPermission confirms a
// RemoteFiles.View-only grant can list/read but never write, matching the
// local Files.View/Files.Edit separation.
func TestRemoteFilesReadOnlyWithoutMutationPermission(t *testing.T) {
	fake := remoteServerCapableFake()
	fake.serversByID = map[string]remote.ServerSummary{"srv-1": sampleServerSummary("srv-1", "default", "Alpha")}
	fake.fileEntries = []remote.FileEntry{{Name: "config.txt", RelativePath: "config.txt", Type: "file"}}
	h, db := newNodeTestServer(t, fake)
	admin := createAdminSession(t, h)
	nodeID := enrollRemoteServerTestNode(t, h, admin, "https://remote-one.internal", "Remote One")

	identities := identity.New(db)
	viewer, err := identities.CreateUser(context.Background(), identity.CreateUserInput{Username: "files-viewer", Email: "files-viewer@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	grantNodePermission(t, db, viewer.ID, "RemoteFiles.View")
	session := loginSession(t, h, viewer.Username)

	if response := templateRequest(h, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-1/files", nil, &session, false); response.Code != http.StatusOK {
		t.Fatalf("list files: %d %s", response.Code, response.Body.String())
	}
	writeBody, _ := json.Marshal(map[string]string{"path": "config.txt", "content": "changed"})
	if response := templateRequest(h, http.MethodPut, "/api/v1/remote-nodes/"+nodeID+"/servers/srv-1/files/content", writeBody, &session, true); response.Code != http.StatusForbidden {
		t.Fatalf("expected write to be forbidden without RemoteFiles.Edit, got %d %s", response.Code, response.Body.String())
	}
}
