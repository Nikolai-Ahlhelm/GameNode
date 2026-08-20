package api_test

import (
	"net/http"
	"testing"
)

func TestTenantStatusPageDisabledPublicAndPrivate(t *testing.T) {
	handler, _ := newTestServer(t)
	admin := createAdminSession(t, handler)

	if response := templateRequest(handler, http.MethodGet, "/api/v1/status/default", nil, nil, false); response.Code != http.StatusNotFound {
		t.Fatalf("disabled page = %d, want 404", response.Code)
	}
	public := templateRequest(handler, http.MethodPatch, "/api/v1/tenants/default", []byte(`{"status_page_enabled":true,"status_page_public":true}`), &admin, true)
	if public.Code != http.StatusOK {
		t.Fatalf("enable public page: %d %s", public.Code, public.Body.String())
	}
	if response := templateRequest(handler, http.MethodGet, "/api/v1/status/default", nil, nil, false); response.Code != http.StatusOK {
		t.Fatalf("public page = %d %s", response.Code, response.Body.String())
	}
	private := templateRequest(handler, http.MethodPatch, "/api/v1/tenants/default", []byte(`{"status_page_public":false}`), &admin, true)
	if private.Code != http.StatusOK {
		t.Fatalf("make page private: %d %s", private.Code, private.Body.String())
	}
	if response := templateRequest(handler, http.MethodGet, "/api/v1/status/default", nil, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous private page = %d, want 401", response.Code)
	}
	if response := templateRequest(handler, http.MethodGet, "/api/v1/status/default", nil, &admin, false); response.Code != http.StatusOK {
		t.Fatalf("admin private page = %d %s", response.Code, response.Body.String())
	}
}
