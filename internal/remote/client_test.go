package remote_test

import (
	"context"
	"encoding/json"
	"errors"
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

func asRemoteError(err error, target **remote.Error) bool {
	if e, ok := err.(*remote.Error); ok {
		*target = e
		return true
	}
	return false
}

func TestStartProvisioningSendsTypedRequestAndReturnsJob(t *testing.T) {
	var receivedPath, receivedAuth string
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"id": "job-1", "runtime_type": "container", "status": "pending"})
	}))
	defer srv.Close()
	c := remote.New()
	job, err := c.StartProvisioning(context.Background(), srv.URL, "cred", remote.ProvisioningRequest{
		TemplateID: "t1", ServerName: "s", DirectoryName: "d", RuntimeType: "container", Image: "ghcr.io/example/game:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receivedPath != "/api/v1/node/provisioning" || receivedAuth != "Bearer cred" {
		t.Fatalf("unexpected request: path=%q auth=%q", receivedPath, receivedAuth)
	}
	if receivedBody["template_id"] != "t1" || receivedBody["image"] != "ghcr.io/example/game:1" {
		t.Fatalf("unexpected forwarded body: %+v", receivedBody)
	}
	if job.ID != "job-1" || job.RuntimeType != "container" {
		t.Fatalf("unexpected job: %+v", job)
	}
}

func TestStartProvisioningReturnsTypedProvisioningError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "container_image_not_declared", "message": "the selected image is not declared by the Egg"}})
	}))
	defer srv.Close()
	c := remote.New()
	_, err := c.StartProvisioning(context.Background(), srv.URL, "cred", remote.ProvisioningRequest{TemplateID: "t1"})
	var provErr *remote.ProvisioningError
	if !errors.As(err, &provErr) {
		t.Fatalf("expected a *remote.ProvisioningError, got %T: %v", err, err)
	}
	if provErr.Code != "container_image_not_declared" || provErr.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unexpected provisioning error: %+v", provErr)
	}
}

func TestStartProvisioningUnauthorizedIsAuthenticationFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := remote.New()
	_, err := c.StartProvisioning(context.Background(), srv.URL, "bad-cred", remote.ProvisioningRequest{TemplateID: "t1"})
	var remoteErr *remote.Error
	if !errors.As(err, &remoteErr) || remoteErr.Kind != remote.KindAuthenticationFailed {
		t.Fatalf("expected KindAuthenticationFailed, got %v", err)
	}
}

func TestGetAndCancelProvisioningJob(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"id": "job-1", "status": "installing"})
	}))
	defer srv.Close()
	c := remote.New()

	job, err := c.GetProvisioningJob(context.Background(), srv.URL, "cred", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/node/provisioning/job-1" || job.ID != "job-1" {
		t.Fatalf("unexpected get: method=%q path=%q job=%+v", gotMethod, gotPath, job)
	}

	job, err = c.CancelProvisioningJob(context.Background(), srv.URL, "cred", "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/node/provisioning/job-1/cancel" || job.ID != "job-1" {
		t.Fatalf("unexpected cancel: method=%q path=%q job=%+v", gotMethod, gotPath, job)
	}
}
