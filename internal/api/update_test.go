package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"gamenode/internal/remote"
	"gamenode/internal/runtime"
	"gamenode/internal/selfupdate"
	"gamenode/internal/servers"
)

type updateFakeSource struct {
	release selfupdate.Release
	files   map[string][]byte
}

func (f *updateFakeSource) Latest(context.Context) (selfupdate.Release, error) { return f.release, nil }
func (f *updateFakeSource) Open(_ context.Context, _ selfupdate.Release, asset string, _ int64) (io.ReadCloser, int64, error) {
	data, ok := f.files[asset]
	if !ok {
		return nil, 0, errors.New("missing")
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

type updateFixture struct {
	handler  http.Handler
	db       *sql.DB
	fake     *fakeRemoteClient
	updater  *selfupdate.Service
	exe      string
	activity *selfupdate.Activity
}

func updateELF() []byte {
	data := make([]byte, 1<<20+64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	data[18] = 0x3E
	copy(data[1<<20:], "api-test-binary")
	return data
}

// newUpdateFixture builds a full API server with a real selfupdate.Service
// driven by a fake release source, a temp "executable", and no network.
func newUpdateFixture(t *testing.T, currentVersion string, withUpdater bool) updateFixture {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	exe := filepath.Join(dir, "gamenode")
	if err = os.WriteFile(exe, []byte("installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := updateELF()
	sum := sha256.Sum256(binary)
	source := &updateFakeSource{
		release: selfupdate.Release{Tag: "v2.0.0", Version: "2.0.0", URL: "https://example.invalid", Assets: []selfupdate.Asset{{Name: "gamenode-linux-amd64", Size: int64(len(binary))}, {Name: selfupdate.ChecksumAsset, Size: 100}}},
		files:   map[string][]byte{"gamenode-linux-amd64": binary, selfupdate.ChecksumAsset: []byte(hex.EncodeToString(sum[:]) + "  gamenode-linux-amd64\n")},
	}
	activity := &selfupdate.Activity{}
	fixture := updateFixture{db: db, fake: &fakeRemoteClient{}, exe: exe, activity: activity}
	options := api.Options{RemoteClient: fixture.fake}
	if withUpdater {
		updater, newErr := selfupdate.New(selfupdate.Options{
			DataDirectory: filepath.Join(dir, "data"), CurrentVersion: currentVersion, Source: source, Executable: exe, GOOS: "linux", GOARCH: "amd64",
			Activity:      func(context.Context) (selfupdate.Activity, error) { return *activity, nil },
			DatabaseCheck: func(context.Context) error { return nil },
			DatabaseBackup: func(_ context.Context, path string) error {
				return os.WriteFile(path, []byte("db"), 0o600)
			},
			FreeBytes:    func(string) (uint64, error) { return 10 << 30, nil },
			SelfTest:     func(context.Context, string) (string, error) { return "2.0.0", nil },
			RestartDelay: time.Hour, // never actually request a restart during a test
		})
		if newErr != nil {
			t.Fatal(newErr)
		}
		fixture.updater = updater
		options.Updater = updater
	}
	fixture.handler = api.New(auth.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative()), slog.New(slog.NewTextHandler(io.Discard, nil)), false, options).Handler(http.NotFoundHandler())
	return fixture
}

func (f updateFixture) userWith(t *testing.T, username string, permissions ...string) testSession {
	t.Helper()
	user, err := identity.New(f.db).CreateUser(context.Background(), identity.CreateUserInput{Username: username, Email: username + "@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range permissions {
		grantNodePermission(t, f.db, user.ID, permission)
	}
	return loginSession(t, f.handler, user.Username)
}

func (f updateFixture) waitReady(t *testing.T, session *testSession) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := templateRequest(f.handler, http.MethodGet, "/api/v1/system/update", nil, session, false)
		var status selfupdate.Status
		_ = json.Unmarshal(response.Body.Bytes(), &status)
		if status.State == selfupdate.StateReady {
			return
		}
		if status.State == selfupdate.StateFailed {
			t.Fatalf("download failed: %+v", status.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("update never became ready")
}

func decodeStatus(t *testing.T, response *httptest.ResponseRecorder) selfupdate.Status {
	t.Helper()
	var status selfupdate.Status
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v: %s", err, response.Body.String())
	}
	return status
}

func TestSystemUpdateRBACAndCSRF(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)
	none := f.userWith(t, "no-update-access")
	viewer := f.userWith(t, "update-viewer", "Update.View")
	manager := f.userWith(t, "update-manager", "Update.Manage")

	if response := templateRequest(f.handler, http.MethodGet, "/api/v1/system/update", nil, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d", response.Code)
	}
	if response := templateRequest(f.handler, http.MethodGet, "/api/v1/system/update", nil, &none, false); response.Code != http.StatusForbidden {
		t.Fatalf("no permission read: %d", response.Code)
	}
	if response := templateRequest(f.handler, http.MethodGet, "/api/v1/system/update", nil, &viewer, false); response.Code != http.StatusOK {
		t.Fatalf("Update.View read: %d %s", response.Code, response.Body.String())
	}
	// Update.Manage does not imply Update.View, and View does not grant install.
	if response := templateRequest(f.handler, http.MethodGet, "/api/v1/system/update", nil, &manager, false); response.Code != http.StatusForbidden {
		t.Fatalf("Update.Manage alone must not read status: %d", response.Code)
	}
	for _, action := range []string{"prepare", "apply"} {
		body := []byte(`{"version":"2.0.0"}`)
		if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/"+action, body, &viewer, true); response.Code != http.StatusForbidden {
			t.Fatalf("Update.View must not allow %s: %d", action, response.Code)
		}
		if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/"+action, body, &admin, false); response.Code != http.StatusForbidden {
			t.Fatalf("%s without CSRF: %d", action, response.Code)
		}
	}
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/check", nil, &viewer, false); response.Code != http.StatusForbidden {
		t.Fatalf("check without CSRF: %d", response.Code)
	}
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/check", nil, &manager, true); response.Code != http.StatusForbidden {
		t.Fatalf("check needs Update.View: %d", response.Code)
	}
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/check", nil, &viewer, true); response.Code != http.StatusOK {
		t.Fatalf("check with Update.View: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(f.handler, http.MethodDelete, "/api/v1/system/update", nil, &admin, true); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected method: %d", response.Code)
	}
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/nonsense", nil, &admin, true); response.Code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", response.Code)
	}
}

func TestSystemUpdateFlowWithSafetyChecksAndAudit(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)

	checked := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/check", nil, &admin, true)
	status := decodeStatus(t, checked)
	if checked.Code != http.StatusOK || !status.UpdateAvailable || status.Available == nil || status.Available.Version != "2.0.0" {
		t.Fatalf("check: %d %s", checked.Code, checked.Body.String())
	}
	if strings.Contains(checked.Body.String(), f.exe) {
		t.Fatal("status must not leak the executable path")
	}

	// Applying before anything is downloaded is refused.
	early := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/apply", []byte(`{"version":"2.0.0"}`), &admin, true)
	if early.Code != http.StatusConflict || !strings.Contains(early.Body.String(), selfupdate.CodeNotReady) {
		t.Fatalf("apply before download: %d %s", early.Code, early.Body.String())
	}

	// A malformed or unknown version never reaches the updater's download path.
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/prepare", []byte(`{"version":"../../etc/passwd"}`), &admin, true); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed version: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/prepare", []byte(`{}`), &admin, true); response.Code != http.StatusBadRequest {
		t.Fatalf("missing version: %d", response.Code)
	}
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/prepare", []byte(`{"version":"9.9.9"}`), &admin, true); response.Code != http.StatusConflict {
		t.Fatalf("unchecked version: %d %s", response.Code, response.Body.String())
	}

	// A running provisioning job blocks the download itself.
	f.activity.ProvisioningJobs = 1
	blocked := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/prepare", []byte(`{"version":"2.0.0"}`), &admin, true)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), `"active_jobs"`) || !strings.Contains(blocked.Body.String(), `"block"`) {
		t.Fatalf("blocked prepare must explain itself with checks: %d %s", blocked.Code, blocked.Body.String())
	}
	f.activity.ProvisioningJobs = 0

	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/prepare", []byte(`{"version":"v2.0.0"}`), &admin, true); response.Code != http.StatusAccepted {
		t.Fatalf("prepare: %d %s", response.Code, response.Body.String())
	}
	f.waitReady(t, &admin)

	// Running servers are a warning: refused without acknowledgement.
	f.activity.RunningServers = 2
	ack := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/apply", []byte(`{"version":"2.0.0"}`), &admin, true)
	if ack.Code != http.StatusConflict || !strings.Contains(ack.Body.String(), selfupdate.CodeAcknowledge) || !strings.Contains(ack.Body.String(), `"running_servers"`) {
		t.Fatalf("unacknowledged apply: %d %s", ack.Code, ack.Body.String())
	}
	// A blocking check cannot be acknowledged away.
	f.activity.TransitionalServers = 1
	hard := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/apply", []byte(`{"version":"2.0.0","acknowledge_warnings":true}`), &admin, true)
	if hard.Code != http.StatusConflict || !strings.Contains(hard.Body.String(), selfupdate.CodePreflightBlocked) {
		t.Fatalf("blocked apply: %d %s", hard.Code, hard.Body.String())
	}
	if data, _ := os.ReadFile(f.exe); string(data) != "installed" {
		t.Fatal("a refused apply must not touch the executable")
	}
	f.activity.TransitionalServers = 0

	done := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/apply", []byte(`{"version":"2.0.0","acknowledge_warnings":true}`), &admin, true)
	if done.Code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", done.Code, done.Body.String())
	}
	if final := decodeStatus(t, done); final.State != selfupdate.StateRestarting {
		t.Fatalf("state after apply: %s", final.State)
	}
	if data, _ := os.ReadFile(f.exe); len(data) < 1<<20 {
		t.Fatal("the new binary should now be installed")
	}

	events, err := audit.New(f.db).List(context.Background(), audit.Filter{Action: audit.SystemUpdateApply})
	if err != nil {
		t.Fatal(err)
	}
	var success, failure int
	for _, event := range events {
		if event.ResourceType != audit.System || event.ActorUsername != "admin" {
			t.Fatalf("unexpected audit attribution: %+v", event)
		}
		switch event.Result {
		case audit.Success:
			success++
			for _, want := range []string{`"from_version":"1.0.0"`, `"to_version":"2.0.0"`, `"warnings_acknowledged":true`} {
				if !strings.Contains(string(event.Metadata), want) {
					t.Fatalf("success audit metadata missing %s: %s", want, event.Metadata)
				}
			}
		case audit.Failure:
			failure++
			if event.ErrorCode == "" {
				t.Fatal("failed apply must carry a controlled error code")
			}
		}
	}
	if success != 1 || failure < 3 {
		t.Fatalf("expected 1 success and every refusal audited, got success=%d failure=%d", success, failure)
	}
}

func TestSystemUpdateUnavailableWithoutUpdater(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", false)
	admin := createAdminSession(t, f.handler)
	if response := templateRequest(f.handler, http.MethodGet, "/api/v1/system/update", nil, &admin, false); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", response.Code)
	}
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/apply", []byte(`{"version":"2.0.0"}`), &admin, true); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("apply: %d", response.Code)
	}
}

func TestSystemUpdateDevBuildIsNotOffered(t *testing.T) {
	f := newUpdateFixture(t, "dev", true)
	admin := createAdminSession(t, f.handler)
	response := templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/check", nil, &admin, true)
	status := decodeStatus(t, response)
	if status.Updatable || status.UpdateAvailable || status.CanPrepare {
		t.Fatalf("dev build must not be updatable: %+v", status)
	}
	if response = templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/prepare", []byte(`{"version":"2.0.0"}`), &admin, true); response.Code != http.StatusConflict {
		t.Fatalf("prepare on dev build: %d %s", response.Code, response.Body.String())
	}
}

// --- machine-authenticated node endpoints -----------------------------------

func machineCredential(t *testing.T, f updateFixture, admin *testSession) string {
	t.Helper()
	pairing := templateRequest(f.handler, http.MethodPost, "/api/v1/node/pairing-tokens", nil, admin, true)
	var token struct {
		PairingToken string `json:"pairing_token"`
	}
	if err := json.Unmarshal(pairing.Body.Bytes(), &token); err != nil || token.PairingToken == "" {
		t.Fatalf("pairing token: %d %s", pairing.Code, pairing.Body.String())
	}
	enroll := httptest.NewRequest(http.MethodPost, "/api/v1/node/enroll", bytes.NewBufferString(`{"pairing_token":"`+token.PairingToken+`"}`))
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, enroll)
	var enrolled struct {
		Credential   string   `json:"credential"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &enrolled); err != nil || enrolled.Credential == "" {
		t.Fatalf("enroll: %d %s", response.Code, response.Body.String())
	}
	found := false
	for _, capability := range enrolled.Capabilities {
		found = found || capability == "self_update"
	}
	if !found {
		t.Fatalf("a build with self-update must advertise the self_update capability: %v", enrolled.Capabilities)
	}
	return enrolled.Credential
}

func machineRequest(f updateFixture, method, path, credential string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func TestNodeUpdateEndpointsRequireMachineCredentialNotBrowserSession(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)
	credential := machineCredential(t, f, &admin)

	for _, path := range []string{"/api/v1/node/update", "/api/v1/node/update/check"} {
		verb := http.MethodGet
		if strings.HasSuffix(path, "check") {
			verb = http.MethodPost
		}
		if response := machineRequest(f, verb, path, "", nil); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s without credential: %d", path, response.Code)
		}
		if response := machineRequest(f, verb, path, "not-a-credential", nil); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s with bad credential: %d", path, response.Code)
		}
		// An administrator's browser session (even with CSRF) is not a machine credential.
		if response := templateRequest(f.handler, verb, path, nil, &admin, true); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s with browser session: %d", path, response.Code)
		}
	}
	for _, action := range []string{"prepare", "apply"} {
		if response := templateRequest(f.handler, http.MethodPost, "/api/v1/node/update/"+action, []byte(`{"version":"2.0.0"}`), &admin, true); response.Code != http.StatusUnauthorized {
			t.Fatalf("browser session must not drive node %s: %d", action, response.Code)
		}
	}

	if response := machineRequest(f, http.MethodGet, "/api/v1/node/update", credential, nil); response.Code != http.StatusOK {
		t.Fatalf("status with credential: %d %s", response.Code, response.Body.String())
	}
	checked := machineRequest(f, http.MethodPost, "/api/v1/node/update/check", credential, nil)
	if status := decodeStatus(t, checked); !status.UpdateAvailable {
		t.Fatalf("check: %d %s", checked.Code, checked.Body.String())
	}
	if response := machineRequest(f, http.MethodPost, "/api/v1/node/update/prepare", credential, []byte(`{"version":"2.0.0"}`)); response.Code != http.StatusAccepted {
		t.Fatalf("prepare: %d %s", response.Code, response.Body.String())
	}
	f.waitReady(t, &admin)

	// The controller cannot smuggle a binary or URL: the request only carries a
	// version, and unknown fields are refused outright.
	if response := machineRequest(f, http.MethodPost, "/api/v1/node/update/apply", credential, []byte(`{"version":"2.0.0","url":"https://evil.example/gamenode"}`)); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown request fields must be rejected: %d %s", response.Code, response.Body.String())
	}
	f.activity.ServerUpdateJobs = 1
	if response := machineRequest(f, http.MethodPost, "/api/v1/node/update/apply", credential, []byte(`{"version":"2.0.0"}`)); response.Code != http.StatusConflict {
		t.Fatalf("node-side safety checks must apply to a controller request: %d %s", response.Code, response.Body.String())
	}
	f.activity.ServerUpdateJobs = 0
	if response := machineRequest(f, http.MethodPost, "/api/v1/node/update/apply", credential, []byte(`{"version":"2.0.0"}`)); response.Code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", response.Code, response.Body.String())
	}
	events, err := audit.New(f.db).List(context.Background(), audit.Filter{Action: audit.SystemUpdateApply})
	if err != nil {
		t.Fatal(err)
	}
	var sawSuccess bool
	for _, event := range events {
		if event.Result == audit.Success {
			sawSuccess = true
			if event.ActorUserID != nil || !strings.Contains(string(event.Metadata), `"initiator":"controller"`) {
				t.Fatalf("a controller-initiated install must be attributed to the controller, not a user: %+v %s", event, event.Metadata)
			}
		}
	}
	if !sawSuccess {
		t.Fatal("the node must audit an install a controller requested")
	}
	if response := machineRequest(f, http.MethodDelete, "/api/v1/node/update", credential, nil); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected method: %d", response.Code)
	}
}

// --- controller side ---------------------------------------------------------

func enrollUpdateNode(t *testing.T, f updateFixture, admin *testSession, capabilities []string) string {
	t.Helper()
	f.fake.enrollResult = remote.EnrollResult{NodeID: "remote-update-node", DisplayName: "Remote", Credential: "remote-secret-credential", ProtocolVersion: 1, GameNodeVersion: "1.0.0", OS: "linux", Arch: "amd64", Capabilities: capabilities}
	response := templateRequest(f.handler, http.MethodPost, "/api/v1/remote-nodes", []byte(`{"endpoint":"https://remote.internal:8443","pairing_token":"x","display_name":"Remote"}`), admin, true)
	var body struct {
		RemoteNode struct {
			ID string `json:"id"`
		} `json:"remote_node"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.RemoteNode.ID == "" {
		t.Fatalf("enroll: %d %s", response.Code, response.Body.String())
	}
	return body.RemoteNode.ID
}

func TestRemoteNodeUpdatePermissionsRequireBothNodeAndUpdate(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)
	nodeID := enrollUpdateNode(t, f, &admin, []string{"self_update", "console"})
	f.fake.selfUpdateStatus = selfupdate.Status{CurrentVersion: "1.0.0", State: selfupdate.StateIdle}
	base := "/api/v1/remote-nodes/" + nodeID + "/update"

	nodeViewOnly := f.userWith(t, "node-view-only", "Node.View")
	updateViewOnly := f.userWith(t, "update-view-only", "Update.View")
	viewBoth := f.userWith(t, "view-both", "Node.View", "Update.View")
	nodeManageOnly := f.userWith(t, "node-manage-only", "Node.View", "Update.View", "Node.Manage")
	updateManageOnly := f.userWith(t, "update-manage-only", "Node.View", "Update.View", "Update.Manage")
	manageBoth := f.userWith(t, "manage-both", "Node.Manage", "Update.Manage")

	for name, session := range map[string]*testSession{"node view only": &nodeViewOnly, "update view only": &updateViewOnly} {
		if response := templateRequest(f.handler, http.MethodGet, base, nil, session, false); response.Code != http.StatusForbidden {
			t.Fatalf("%s must not read a node's update status: %d", name, response.Code)
		}
	}
	if response := templateRequest(f.handler, http.MethodGet, base, nil, &viewBoth, false); response.Code != http.StatusOK {
		t.Fatalf("view both: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(f.handler, http.MethodPost, base+"/check", nil, &viewBoth, true); response.Code != http.StatusOK {
		t.Fatalf("check with both view permissions: %d %s", response.Code, response.Body.String())
	}
	for _, action := range []string{"prepare", "apply", "cancel"} {
		body := []byte(`{"version":"2.0.0"}`)
		for name, session := range map[string]*testSession{"view both": &viewBoth, "node manage only": &nodeManageOnly, "update manage only": &updateManageOnly} {
			if response := templateRequest(f.handler, http.MethodPost, base+"/"+action, body, session, true); response.Code != http.StatusForbidden {
				t.Fatalf("%s must not %s: %d %s", name, action, response.Code, response.Body.String())
			}
		}
		if response := templateRequest(f.handler, http.MethodPost, base+"/"+action, body, &manageBoth, false); response.Code != http.StatusForbidden {
			t.Fatalf("%s without CSRF: %d", action, response.Code)
		}
	}
	if len(f.fake.updateCalls) != 2 {
		t.Fatalf("only the two permitted reads may reach the node, got %v", f.fake.updateCalls)
	}
	if response := templateRequest(f.handler, http.MethodPost, base+"/apply", []byte(`{"version":"2.0.0","acknowledge_warnings":true}`), &manageBoth, true); response.Code != http.StatusAccepted {
		t.Fatalf("apply with both manage permissions: %d %s", response.Code, response.Body.String())
	}
	if last := f.fake.updateCalls[len(f.fake.updateCalls)-1]; last != "apply:2.0.0" || !f.fake.lastUpdateAck {
		t.Fatalf("apply not forwarded faithfully: %v ack=%v", f.fake.updateCalls, f.fake.lastUpdateAck)
	}
	events, err := audit.New(f.db).List(context.Background(), audit.Filter{Action: audit.NodeSoftwareUpdate})
	if err != nil || len(events) != 1 || events[0].Result != audit.Success || events[0].ActorUsername != "manage-both" || !strings.Contains(string(events[0].Metadata), `"to_version":"2.0.0"`) {
		t.Fatalf("controller-side audit: %+v %v", events, err)
	}
}

func TestRemoteNodeUpdateRequiresCapabilityAndEnabledNode(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)
	old := enrollUpdateNode(t, f, &admin, []string{"console"})
	base := "/api/v1/remote-nodes/" + old + "/update"

	read := templateRequest(f.handler, http.MethodGet, base, nil, &admin, false)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"supported":false`) {
		t.Fatalf("a node without the capability must report unsupported: %d %s", read.Code, read.Body.String())
	}
	apply := templateRequest(f.handler, http.MethodPost, base+"/apply", []byte(`{"version":"2.0.0"}`), &admin, true)
	if apply.Code != http.StatusConflict || !strings.Contains(apply.Body.String(), "remote_update_unsupported") {
		t.Fatalf("apply on an old node: %d %s", apply.Code, apply.Body.String())
	}
	if len(f.fake.updateCalls) != 0 {
		t.Fatalf("an unsupported node must never be contacted: %v", f.fake.updateCalls)
	}

	supported := enrollUpdateNodeWith(t, f, &admin, "remote-2", "https://remote2.internal:8443", []string{"self_update"})
	disable := templateRequest(f.handler, http.MethodPatch, "/api/v1/remote-nodes/"+supported, []byte(`{"enabled":false}`), &admin, true)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", disable.Code, disable.Body.String())
	}
	apply = templateRequest(f.handler, http.MethodPost, "/api/v1/remote-nodes/"+supported+"/update/apply", []byte(`{"version":"2.0.0"}`), &admin, true)
	if apply.Code != http.StatusConflict || !strings.Contains(apply.Body.String(), "remote_node_disabled") {
		t.Fatalf("apply on a disabled node: %d %s", apply.Code, apply.Body.String())
	}
	if response := templateRequest(f.handler, http.MethodPost, "/api/v1/remote-nodes/does-not-exist/update/check", nil, &admin, true); response.Code != http.StatusNotFound {
		t.Fatalf("unknown node: %d", response.Code)
	}
}

func enrollUpdateNodeWith(t *testing.T, f updateFixture, admin *testSession, nodeID, endpoint string, capabilities []string) string {
	t.Helper()
	f.fake.enrollResult = remote.EnrollResult{NodeID: nodeID, DisplayName: nodeID, Credential: "credential-" + nodeID, ProtocolVersion: 1, GameNodeVersion: "1.0.0", OS: "linux", Arch: "amd64", Capabilities: capabilities}
	response := templateRequest(f.handler, http.MethodPost, "/api/v1/remote-nodes", []byte(`{"endpoint":"`+endpoint+`","pairing_token":"x","display_name":"`+nodeID+`"}`), admin, true)
	var body struct {
		RemoteNode struct {
			ID string `json:"id"`
		} `json:"remote_node"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.RemoteNode.ID == "" {
		t.Fatalf("enroll: %d %s", response.Code, response.Body.String())
	}
	return body.RemoteNode.ID
}

func TestRemoteNodeUpdateErrorsNeverEchoRemoteText(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)
	nodeID := enrollUpdateNode(t, f, &admin, []string{"self_update"})
	url := "/api/v1/remote-nodes/" + nodeID + "/update/apply"
	body := []byte(`{"version":"2.0.0"}`)

	// A known refusal keeps its stable code and (bounded) safety checks, but the
	// human text is this controller's own, never the remote node's.
	huge := strings.Repeat("x", 5000)
	f.fake.selfUpdateErr = &remote.UpdateError{StatusCode: 409, Code: selfupdate.CodePreflightBlocked, Message: "PWNED remote message", Checks: []selfupdate.Check{
		{ID: "active_jobs", Label: "Jobs", Status: selfupdate.CheckBlock, Message: huge},
		{ID: "weird", Label: "Weird", Status: "definitely-fine", Message: "m"},
	}}
	response := templateRequest(f.handler, http.MethodPost, url, body, &admin, true)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "PWNED") || !strings.Contains(response.Body.String(), selfupdate.CodePreflightBlocked) {
		t.Fatalf("known code: %d %s", response.Code, response.Body.String())
	}
	var parsed struct {
		Checks []selfupdate.Check `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil || len(parsed.Checks) != 2 {
		t.Fatalf("checks: %v %s", err, response.Body.String())
	}
	if len(parsed.Checks[0].Message) > 400 {
		t.Fatalf("remote check text must be bounded, got %d bytes", len(parsed.Checks[0].Message))
	}
	if parsed.Checks[1].Status != selfupdate.CheckBlock {
		t.Fatalf("an unrecognized check status must be treated as a block, got %q", parsed.Checks[1].Status)
	}

	// An unknown code from the node is reported generically.
	f.fake.selfUpdateErr = &remote.UpdateError{StatusCode: 500, Code: "<script>alert(1)</script>", Message: "PWNED"}
	response = templateRequest(f.handler, http.MethodPost, url, body, &admin, true)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "PWNED") || strings.Contains(response.Body.String(), "script") {
		t.Fatalf("unknown code: %d %s", response.Code, response.Body.String())
	}

	// Transport failures use the existing controlled codes.
	f.fake.selfUpdateErr = &remote.Error{Kind: remote.KindUnreachable, Detail: "dial tcp 10.0.0.5:8443: secret internal detail"}
	response = templateRequest(f.handler, http.MethodPost, url, body, &admin, true)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "node_unreachable") || strings.Contains(response.Body.String(), "10.0.0.5") {
		t.Fatalf("unreachable: %d %s", response.Code, response.Body.String())
	}

	events, err := audit.New(f.db).List(context.Background(), audit.Filter{Action: audit.NodeSoftwareUpdate})
	if err != nil || len(events) != 3 {
		t.Fatalf("every remote install attempt must be audited: %d %v", len(events), err)
	}
	for _, event := range events {
		if event.Result != audit.Failure || event.ErrorCode == "" || strings.Contains(event.ErrorSummary, "PWNED") || strings.Contains(event.ErrorSummary, "10.0.0.5") {
			t.Fatalf("audit must carry controlled failure text only: %+v", event)
		}
	}
}

func TestRemoteNodeUpdateStatusIsClampedAndNeverLeaksCredential(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)
	nodeID := enrollUpdateNode(t, f, &admin, []string{"self_update"})
	checks := make([]selfupdate.Check, 100)
	for i := range checks {
		checks[i] = selfupdate.Check{ID: "c", Label: "l", Status: selfupdate.CheckPass, Message: "m"}
	}
	f.fake.selfUpdateStatus = selfupdate.Status{CurrentVersion: "1.0.0", State: selfupdate.StateIdle, Checks: checks, Available: &selfupdate.Release{Version: "2.0.0", Notes: strings.Repeat("n", 100000)}}
	response := templateRequest(f.handler, http.MethodGet, "/api/v1/remote-nodes/"+nodeID+"/update", nil, &admin, false)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "remote-secret-credential") {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	var parsed struct {
		Supported bool              `json:"supported"`
		Update    selfupdate.Status `json:"update"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil || !parsed.Supported {
		t.Fatalf("%v %s", err, response.Body.String())
	}
	if len(parsed.Update.Checks) != 32 || len(parsed.Update.Available.Notes) > selfupdate.MaxNotesBytes {
		t.Fatalf("remote status was not clamped: %d checks, %d note bytes", len(parsed.Update.Checks), len(parsed.Update.Available.Notes))
	}
}

func TestAdvertisedCapabilityMatchesEndpoints(t *testing.T) {
	// The self_update capability is only truthful because the endpoints exist;
	// this guards against removing one without the other.
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)
	credential := machineCredential(t, f, &admin)
	response := machineRequest(f, http.MethodGet, "/api/v1/node/capabilities", credential, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"self_update"`) {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
}

func TestSystemUpdateSummaryOmitsChecks(t *testing.T) {
	f := newUpdateFixture(t, "v1.0.0", true)
	admin := createAdminSession(t, f.handler)
	_ = templateRequest(f.handler, http.MethodPost, "/api/v1/system/update/check", nil, &admin, true)
	summary := decodeStatus(t, templateRequest(f.handler, http.MethodGet, "/api/v1/system/update?summary=1", nil, &admin, false))
	if !summary.UpdateAvailable || len(summary.Checks) != 0 {
		t.Fatalf("summary: %+v", summary)
	}
	full := decodeStatus(t, templateRequest(f.handler, http.MethodGet, "/api/v1/system/update", nil, &admin, false))
	if len(full.Checks) == 0 {
		t.Fatal("the default status includes the safety checks")
	}
	viewer := f.userWith(t, "summary-nobody")
	if response := templateRequest(f.handler, http.MethodGet, "/api/v1/system/update?summary=1", nil, &viewer, false); response.Code != http.StatusForbidden {
		t.Fatalf("summary needs Update.View too: %d", response.Code)
	}
}
