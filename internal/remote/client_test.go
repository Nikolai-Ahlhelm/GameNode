package remote_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gamenode/internal/remote"
)

func TestValidateEndpoint(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		want    string
	}{
		{"https://node.internal:8443", false, "https://node.internal:8443"},
		{"http://127.0.0.1:8080", false, "http://127.0.0.1:8080"},
		{"  https://node.internal  ", false, "https://node.internal"},
		{"ftp://node.internal", true, ""},
		{"https://user:pass@node.internal", true, ""},
		{"https://node.internal/some/path", true, ""},
		{"https://node.internal?x=1", true, ""},
		{"", true, ""},
		{"not a url at all ::", true, ""},
	}
	for _, tc := range cases {
		got, err := remote.ValidateEndpoint(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateEndpoint(%q): expected error, got %q", tc.in, got)
		}
		if !tc.wantErr && (err != nil || got != tc.want) {
			t.Errorf("ValidateEndpoint(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
		}
	}
}

func TestGetNodeInfoValidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-credential" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(remote.NodeInfo{NodeID: "abc", ProtocolVersion: 1, Capabilities: []string{"console"}})
	}))
	defer srv.Close()
	c := remote.New()
	info, err := c.GetNodeInfo(context.Background(), srv.URL, "good-credential")
	if err != nil {
		t.Fatal(err)
	}
	if info.NodeID != "abc" || info.ProtocolVersion != 1 {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestGetNodeInfoUnreachable(t *testing.T) {
	c := remote.New()
	_, err := c.GetNodeInfo(context.Background(), "http://127.0.0.1:1", "cred")
	var remoteErr *remote.Error
	if err == nil {
		t.Fatal("expected an error")
	}
	if !asRemoteError(err, &remoteErr) || remoteErr.Kind != remote.KindUnreachable {
		t.Fatalf("expected KindUnreachable, got %v", err)
	}
}

func TestGetNodeInfoTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	c := remote.New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.GetNodeInfo(ctx, srv.URL, "cred")
	var remoteErr *remote.Error
	if !asRemoteError(err, &remoteErr) || remoteErr.Kind != remote.KindUnreachable {
		t.Fatalf("expected KindUnreachable on timeout, got %v", err)
	}
}

func TestGetNodeInfoAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := remote.New()
	_, err := c.GetNodeInfo(context.Background(), srv.URL, "bad")
	var remoteErr *remote.Error
	if !asRemoteError(err, &remoteErr) || remoteErr.Kind != remote.KindAuthenticationFailed {
		t.Fatalf("expected KindAuthenticationFailed, got %v", err)
	}
}

func TestGetNodeInfoProtocolIncompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer srv.Close()
	c := remote.New()
	_, err := c.GetNodeInfo(context.Background(), srv.URL, "cred")
	var remoteErr *remote.Error
	if !asRemoteError(err, &remoteErr) || remoteErr.Kind != remote.KindProtocolIncompatible {
		t.Fatalf("expected KindProtocolIncompatible, got %v", err)
	}
}

func TestGetNodeInfoMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	c := remote.New()
	_, err := c.GetNodeInfo(context.Background(), srv.URL, "cred")
	var remoteErr *remote.Error
	if !asRemoteError(err, &remoteErr) || remoteErr.Kind != remote.KindMalformedResponse {
		t.Fatalf("expected KindMalformedResponse, got %v", err)
	}
}

func TestGetNodeInfoOversizedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", remote.MaxResponseBytes+10)))
	}))
	defer srv.Close()
	c := remote.New()
	_, err := c.GetNodeInfo(context.Background(), srv.URL, "cred")
	var remoteErr *remote.Error
	if !asRemoteError(err, &remoteErr) || remoteErr.Kind != remote.KindOversizedResponse {
		t.Fatalf("expected KindOversizedResponse, got %v", err)
	}
}

// TestRedirectDoesNotForwardCredentialToAnotherHost verifies the client
// stops at the first response instead of re-issuing the (Authorization-
// bearing) request against a redirect target, which could otherwise leak
// the machine credential to an arbitrary host.
func TestRedirectDoesNotForwardCredentialToAnotherHost(t *testing.T) {
	otherHostHit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHostHit = true
		if r.Header.Get("Authorization") != "" {
			t.Error("credential must never be forwarded to a redirect target")
		}
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/v1/node/info", http.StatusFound)
	}))
	defer origin.Close()
	c := remote.New()
	_, err := c.GetNodeInfo(context.Background(), origin.URL, "secret-credential")
	if err == nil {
		t.Fatal("expected redirect response to be treated as an error, not silently followed")
	}
	if otherHostHit {
		t.Fatal("client must not follow a cross-host redirect")
	}
}

func TestEnrollDoesNotSendAuthorizationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("enrollment must not send a prior Authorization header")
		}
		json.NewEncoder(w).Encode(remote.EnrollResult{NodeID: "n", Credential: "issued", ProtocolVersion: 1})
	}))
	defer srv.Close()
	c := remote.New()
	result, err := c.Enroll(context.Background(), srv.URL, "pairing-token")
	if err != nil {
		t.Fatal(err)
	}
	if result.Credential != "issued" {
		t.Fatalf("unexpected enroll result: %+v", result)
	}
}

// --- Remote Server Management (v0.5B) / Operational Hardening (v0.5C) ---

func TestListAndGetServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/node/servers":
			json.NewEncoder(w).Encode(map[string]any{"servers": []remote.ServerSummary{{ID: "s1", TenantID: "default", Name: "Alpha"}}})
		case "/api/v1/node/servers/s1":
			json.NewEncoder(w).Encode(map[string]any{"server": remote.ServerSummary{ID: "s1", TenantID: "default", Name: "Alpha"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := remote.New()
	list, err := c.ListServers(context.Background(), srv.URL, "cred")
	if err != nil || len(list) != 1 || list[0].ID != "s1" {
		t.Fatalf("ListServers: %v, %+v", err, list)
	}
	one, err := c.GetServer(context.Background(), srv.URL, "cred", "s1")
	if err != nil || one.Name != "Alpha" {
		t.Fatalf("GetServer: %v, %+v", err, one)
	}
}

func TestGetServerNotFoundMapsToResourceNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := remote.New()
	_, err := c.GetServer(context.Background(), srv.URL, "cred", "missing")
	var remoteErr *remote.Error
	if !asRemoteError(err, &remoteErr) || remoteErr.Kind != remote.KindResourceNotFound {
		t.Fatalf("expected KindResourceNotFound, got %v", err)
	}
}

func TestStartServerConflictMapsToResourceConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c := remote.New()
	_, err := c.StartServer(context.Background(), srv.URL, "cred", "s1")
	var remoteErr *remote.Error
	if !asRemoteError(err, &remoteErr) || remoteErr.Kind != remote.KindResourceConflict {
		t.Fatalf("expected KindResourceConflict, got %v", err)
	}
}

func TestSendConsoleInputEncodesBodyAndPath(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer srv.Close()
	c := remote.New()
	if err := c.SendConsoleInput(context.Background(), srv.URL, "cred", "s1", "say hi\n"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/node/servers/s1/console" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if !strings.Contains(gotBody, "say hi") {
		t.Fatalf("expected console input in request body, got %s", gotBody)
	}
}

func TestListFilesEscapesQueryPath(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"entries": []remote.FileEntry{}})
	}))
	defer srv.Close()
	c := remote.New()
	if _, err := c.ListFiles(context.Background(), srv.URL, "cred", "s1", "sub dir/a"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "path=sub+dir%2Fa") {
		t.Fatalf("expected escaped path query, got %q", gotQuery)
	}
}

func TestGetAndUpdateConfiguration(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
		}
		json.NewEncoder(w).Encode(remote.RemoteConfiguration{Available: true, Adapters: []remote.RemoteConfigAdapter{{ID: "server-properties", Version: "1", Format: "ini-key-values", Fields: []remote.RemoteConfigField{{Key: "difficulty", Type: "string", Value: "normal"}}}}})
	}))
	defer srv.Close()
	c := remote.New()
	result, err := c.GetConfiguration(context.Background(), srv.URL, "cred", "s1")
	if err != nil || gotMethod != http.MethodGet || gotPath != "/api/v1/node/servers/s1/configuration" {
		t.Fatalf("GetConfiguration: %v, method=%s path=%s", err, gotMethod, gotPath)
	}
	if !result.Available || len(result.Adapters) != 1 || result.Adapters[0].Fields[0].Value != "normal" {
		t.Fatalf("unexpected configuration result: %+v", result)
	}
	if _, err := c.UpdateConfiguration(context.Background(), srv.URL, "cred", "s1", "server-properties", map[string]string{"difficulty": "hard"}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/api/v1/node/servers/s1/configuration" {
		t.Fatalf("unexpected update request: method=%s path=%s", gotMethod, gotPath)
	}
	if !strings.Contains(gotBody, `"adapter_id":"server-properties"`) || !strings.Contains(gotBody, `"difficulty":"hard"`) {
		t.Fatalf("unexpected update body: %s", gotBody)
	}
}

func asRemoteError(err error, target **remote.Error) bool {
	if e, ok := err.(*remote.Error); ok {
		*target = e
		return true
	}
	return false
}

func TestUpdateCallsUseFixedPathsAndTypedErrors(t *testing.T) {
	var paths []string
	var bodies []string
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		auth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		switch {
		case strings.HasSuffix(r.URL.Path, "/apply") && strings.Contains(string(data), "blocked"):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"preflight_blocked","message":"node text"},"checks":[{"id":"active_jobs","label":"Jobs","status":"block","message":"1 job running"}]}`))
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(`{"current_version":"1.0.0","state":"idle","update_available":true,"checks":[]}`))
		}
	}))
	defer server.Close()
	client := remote.New()
	ctx := context.Background()

	status, err := client.GetUpdateStatus(ctx, server.URL, "machine-secret")
	if err != nil || status.CurrentVersion != "1.0.0" || !status.UpdateAvailable {
		t.Fatalf("status: %+v %v", status, err)
	}
	if auth != "Bearer machine-secret" {
		t.Fatalf("machine credential not sent: %q", auth)
	}
	if _, err = client.CheckUpdate(ctx, server.URL, "c"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.PrepareUpdate(ctx, server.URL, "c", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ApplyUpdate(ctx, server.URL, "c", "2.0.0", true); err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"GET /api/v1/node/update", "POST /api/v1/node/update/check", "POST /api/v1/node/update/prepare", "POST /api/v1/node/update/apply"}
	if strings.Join(paths, "|") != strings.Join(wantPaths, "|") {
		t.Fatalf("paths %v", paths)
	}
	if bodies[2] != `{"version":"2.0.0"}` || bodies[3] != `{"acknowledge_warnings":true,"version":"2.0.0"}` {
		t.Fatalf("request bodies carry only a version (and the acknowledgement): %v", bodies)
	}

	// A refusal becomes a typed error carrying the node's checks.
	_, err = client.ApplyUpdate(ctx, server.URL, "c", "blocked", false)
	var updateErr *remote.UpdateError
	if !errorsAs(err, &updateErr) || updateErr.Code != "preflight_blocked" || updateErr.StatusCode != 409 || len(updateErr.Checks) != 1 || updateErr.Checks[0].ID != "active_jobs" {
		t.Fatalf("typed error: %v", err)
	}
	// A node built before self-update has no endpoint: a distinct, controlled kind.
	_, err = client.CancelUpdate(ctx, server.URL, "c")
	var remoteErr *remote.Error
	if !errorsAs(err, &remoteErr) || remoteErr.Kind != remote.KindResourceNotFound {
		t.Fatalf("404 must map to node_resource_not_found: %v", err)
	}
}

func TestUpdateCallsRefuseRedirectsAndBadCredentials(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer target.Close()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer redirecting.Close()
	_, err := remote.New().GetUpdateStatus(context.Background(), redirecting.URL, "secret")
	var remoteErr *remote.Error
	if !errorsAs(err, &remoteErr) || remoteErr.Kind != remote.KindMalformedResponse {
		t.Fatalf("redirect must be refused: %v", err)
	}
	if followed {
		t.Fatal("the client followed a redirect and would have forwarded the credential")
	}
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer unauthorized.Close()
	_, err = remote.New().ApplyUpdate(context.Background(), unauthorized.URL, "bad", "2.0.0", false)
	if !errorsAs(err, &remoteErr) || remoteErr.Kind != remote.KindAuthenticationFailed {
		t.Fatalf("401: %v", err)
	}
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer garbage.Close()
	_, err = remote.New().GetUpdateStatus(context.Background(), garbage.URL, "c")
	if !errorsAs(err, &remoteErr) || remoteErr.Kind != remote.KindMalformedResponse {
		t.Fatalf("garbage body: %v", err)
	}
}

func errorsAs[T any](err error, target *T) bool { return errors.As(err, target) }
