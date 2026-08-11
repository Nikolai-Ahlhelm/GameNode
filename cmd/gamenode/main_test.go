package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesIndexWithoutRedirect(t *testing.T) {
	handler := spaHandler(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<div id=\"root\"></div>")}})

	for _, path := range []string{"/", "/dashboard"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d; want 200", path, response.Code)
		}
	}
}
