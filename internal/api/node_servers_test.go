package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"gamenode/internal/nodeidentity"
)

// nodeMachineCredential drives the real pairing/enroll HTTP flow to obtain a
// machine credential for the Node-facing API under test, exactly like a
// real controller would.
func nodeMachineCredential(t *testing.T, h http.Handler, admin testSession) string {
	t.Helper()
	pairing := templateRequest(h, http.MethodPost, "/api/v1/node/pairing-tokens", nil, &admin, true)
	if pairing.Code != http.StatusOK {
		t.Fatalf("pairing token: %d %s", pairing.Code, pairing.Body.String())
	}
	var pairingBody struct {
		PairingToken string `json:"pairing_token"`
	}
	if err := json.Unmarshal(pairing.Body.Bytes(), &pairingBody); err != nil {
		t.Fatal(err)
	}
	enroll := httptest.NewRequest(http.MethodPost, "/api/v1/node/enroll", bytes.NewBufferString(`{"pairing_token":"`+pairingBody.PairingToken+`"}`))
	enroll.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, enroll)
	if w.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Credential
}

func nodeMachineRequest(h http.Handler, method, path, credential string, body []byte) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestNodeCapabilitiesAdvertiseRemoteServerManagement confirms this build's
// info/capabilities responses list the new v0.5B/v0.5C capabilities, so a
// controller can detect support without guessing from a version number.
func TestNodeCapabilitiesAdvertiseRemoteServerManagement(t *testing.T) {
	h, _ := newNodeTestServer(t, &fakeRemoteClient{})
	admin := createAdminSession(t, h)
	credential := nodeMachineCredential(t, h, admin)
	response := nodeMachineRequest(h, http.MethodGet, "/api/v1/node/capabilities", credential, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		string(nodeidentity.CapabilityRemoteServerManagement): false,
		string(nodeidentity.CapabilityRemoteConsole):          false,
		string(nodeidentity.CapabilityRemoteFiles):            false,
		string(nodeidentity.CapabilityRemoteMonitoring):       false,
	}
	for _, c := range body.Capabilities {
		if _, ok := want[c]; ok {
			want[c] = true
		}
	}
	for capability, found := range want {
		if !found {
			t.Fatalf("expected capability %q to be advertised, got %v", capability, body.Capabilities)
		}
	}
}

// TestNodeServersRequiresMachineAuth confirms the entire /api/v1/node/servers
// surface rejects both an unauthenticated request and a browser session
// alone, exactly like the existing /api/v1/node/info contract.
func TestNodeServersRequiresMachineAuth(t *testing.T) {
	h, _ := newNodeTestServer(t, &fakeRemoteClient{})
	admin := createAdminSession(t, h)
	if response := templateRequest(h, http.MethodGet, "/api/v1/node/servers", nil, &admin, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("browser session alone: %d %s", response.Code, response.Body.String())
	}
	if response := httptest.NewRecorder(); true {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/node/servers", nil)
		h.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated: %d %s", response.Code, response.Body.String())
		}
	}
}

// TestNodeServersLifecycleForwarding drives the machine-authed Node API
// exactly like a controller would: create, list, get, start, stop, delete -
// all forwarded to this node's OWN internal/servers.Service, with a bounded
// response that never leaks the working directory or executable path.
func TestNodeServersLifecycleForwarding(t *testing.T) {
	h, _ := newNodeTestServer(t, &fakeRemoteClient{})
	admin := createAdminSession(t, h)
	credential := nodeMachineCredential(t, h, admin)

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	serverRoot := t.TempDir()
	createBody, _ := json.Marshal(map[string]any{
		"name": "Node-managed", "working_directory": serverRoot, "executable": exe,
		"stop_timeout_seconds": 1, "environment_variables": map[string]string{},
	})
	created := nodeMachineRequest(h, http.MethodPost, "/api/v1/node/servers", credential, createBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	if bytes.Contains(created.Body.Bytes(), []byte(serverRoot)) {
		t.Fatalf("node server response must never include the working directory: %s", created.Body.String())
	}
	var createdBody struct {
		Server struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"server"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	id := createdBody.Server.ID
	if id == "" {
		t.Fatal("expected a created server id")
	}

	list := nodeMachineRequest(h, http.MethodGet, "/api/v1/node/servers", credential, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	if bytes.Contains(list.Body.Bytes(), []byte(serverRoot)) {
		t.Fatalf("node server list must never include the working directory: %s", list.Body.String())
	}

	get := nodeMachineRequest(h, http.MethodGet, "/api/v1/node/servers/"+id, credential, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get: %d %s", get.Code, get.Body.String())
	}

	start := nodeMachineRequest(h, http.MethodPost, "/api/v1/node/servers/"+id+"/start", credential, nil)
	if start.Code != http.StatusOK {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	stop := nodeMachineRequest(h, http.MethodPost, "/api/v1/node/servers/"+id+"/stop", credential, nil)
	if stop.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", stop.Code, stop.Body.String())
	}

	monitoring := nodeMachineRequest(h, http.MethodGet, "/api/v1/node/servers/"+id+"/monitoring", credential, nil)
	if monitoring.Code != http.StatusOK {
		t.Fatalf("monitoring: %d %s", monitoring.Code, monitoring.Body.String())
	}

	console := nodeMachineRequest(h, http.MethodGet, "/api/v1/node/servers/"+id+"/console", credential, nil)
	if console.Code != http.StatusOK {
		t.Fatalf("console snapshot: %d %s", console.Code, console.Body.String())
	}
	consoleSend := nodeMachineRequest(h, http.MethodPost, "/api/v1/node/servers/"+id+"/console", credential, []byte(`{"data":"hello\n"}`))
	if consoleSend.Code != http.StatusConflict {
		t.Fatalf("expected 409 sending console input with no attached session, got %d %s", consoleSend.Code, consoleSend.Body.String())
	}

	files := nodeMachineRequest(h, http.MethodGet, "/api/v1/node/servers/"+id+"/files", credential, nil)
	if files.Code != http.StatusOK {
		t.Fatalf("files list: %d %s", files.Code, files.Body.String())
	}

	deleted := nodeMachineRequest(h, http.MethodDelete, "/api/v1/node/servers/"+id, credential, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	missing := nodeMachineRequest(h, http.MethodGet, "/api/v1/node/servers/"+id, credential, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d %s", missing.Code, missing.Body.String())
	}
}
