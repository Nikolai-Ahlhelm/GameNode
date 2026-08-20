package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesIndexWithoutRedirect(t *testing.T) {
	handler := spaHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<div id=\"root\"></div>")}})

	for _, path := range []string{"/", "/dashboard", "/server/example-id", "/tenants/example-id/members"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d; want 200", path, response.Code)
		}
	}
}

func TestSPAHandlerDoesNotHandleMutations(t *testing.T) {
	handler := spaHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<div id=\"root\"></div>")}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/dashboard", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("POST returned %d; want 404", response.Code)
	}
}
