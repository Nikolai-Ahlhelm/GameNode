package remote_test

import (
	"context"
	"encoding/json"
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

func asRemoteError(err error, target **remote.Error) bool {
	if e, ok := err.(*remote.Error); ok {
		*target = e
		return true
	}
	return false
}
