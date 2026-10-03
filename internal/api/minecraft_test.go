package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/identity"
	"gamenode/internal/rbac"
)

func modJar(t *testing.T, id, name string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, _ := writer.Create("fabric.mod.json")
	entry.Write([]byte(`{"id":"` + id + `","name":"` + name + `","version":"1.2.3"}`))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func (f filesFixture) uploadMod(filename string, data []byte, overwrite bool) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", filename)
	part.Write(data)
	writer.Close()
	query := url.Values{}
	if overwrite {
		query.Set("overwrite", "true")
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers/"+f.server+"/mods?"+query.Encode(), &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", f.csrf)
	request.AddCookie(f.cookie)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func TestModsAPIListAddRemove(t *testing.T) {
	fixture := newFilesFixture(t)
	base := "/api/v1/servers/" + fixture.server + "/mods"
	if response := fixture.request(http.MethodGet, base, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: %d", response.Code)
	}
	var empty struct {
		Available bool             `json:"available"`
		Mods      []map[string]any `json:"mods"`
	}
	response := fixture.request(http.MethodGet, base, true)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &empty) != nil || empty.Available || len(empty.Mods) != 0 {
		t.Fatalf("empty list: %d %s", response.Code, response.Body.String())
	}
	if response = fixture.uploadMod("sodium.jar", modJar(t, "sodium", "Sodium"), false); response.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "mods", "sodium.jar")); err != nil {
		t.Fatalf("mod not stored below the server root: %v", err)
	}
	if response = fixture.uploadMod("sodium.jar", modJar(t, "sodium", "Sodium"), false); response.Code != http.StatusConflict {
		t.Fatalf("duplicate add: %d", response.Code)
	}
	if response = fixture.uploadMod("sodium.jar", modJar(t, "sodium", "Sodium"), true); response.Code != http.StatusCreated {
		t.Fatalf("overwrite add: %d", response.Code)
	}
	if response = fixture.uploadMod("evil.exe", []byte("MZ"), false); response.Code != http.StatusBadRequest {
		t.Fatalf("non-jar name: %d", response.Code)
	}
	if response = fixture.uploadMod("fake.jar", []byte("not a zip file at all"), false); response.Code != http.StatusBadRequest {
		t.Fatalf("non-zip content: %d", response.Code)
	}
	var listed struct {
		Available bool `json:"available"`
		Mods      []struct {
			FileName string   `json:"file_name"`
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Version  string   `json:"version"`
			Loaders  []string `json:"loaders"`
		} `json:"mods"`
	}
	response = fixture.request(http.MethodGet, base, true)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &listed) != nil || !listed.Available || len(listed.Mods) != 1 || listed.Mods[0].ID != "sodium" || listed.Mods[0].Name != "Sodium" || listed.Mods[0].Version != "1.2.3" || listed.Mods[0].Loaders[0] != "fabric" {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), fixture.root) {
		t.Fatal("mod list leaked the host path")
	}
	remove := func(name string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodDelete, base+"?file="+url.QueryEscape(name), nil)
		request.Header.Set("X-CSRF-Token", fixture.csrf)
		request.AddCookie(fixture.cookie)
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		return recorder
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../keep.txt", "keep.txt", "..%2Fkeep.jar", ""} {
		if response = remove(bad); response.Code != http.StatusBadRequest {
			t.Fatalf("remove %q must be rejected: %d", bad, response.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "keep.txt")); err != nil {
		t.Fatal("a rejected removal deleted an unrelated file")
	}
	if response = remove("missing.jar"); response.Code != http.StatusNotFound {
		t.Fatalf("remove missing: %d", response.Code)
	}
	if response = remove("sodium.jar"); response.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "mods", "sodium.jar")); !os.IsNotExist(err) {
		t.Fatal("mod still present after removal")
	}
}

func TestModsAPIRequiresIndependentFilePermissionsAndCSRF(t *testing.T) {
	fixture := newFilesFixture(t)
	ctx := context.Background()
	member, err := identity.New(fixture.db).CreateUser(ctx, identity.CreateUserInput{Username: "modder", Email: "modder@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"modder","password":"a password long enough"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	fixture.handler.ServeHTTP(loginResponse, login)
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err = json.Unmarshal(loginResponse.Body.Bytes(), &session); err != nil || loginResponse.Code != http.StatusOK {
		t.Fatalf("login: %d %v", loginResponse.Code, err)
	}
	cookie := loginResponse.Result().Cookies()[0]
	do := func(method string, body []byte, contentType, query string, csrf bool) int {
		request := httptest.NewRequest(method, "/api/v1/servers/"+fixture.server+"/mods"+query, bytes.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		if csrf {
			request.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	multipartBody := func() ([]byte, string) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, _ := writer.CreateFormFile("file", "a.jar")
		part.Write(modJar(t, "a", "A"))
		writer.Close()
		return body.Bytes(), writer.FormDataContentType()
	}
	grant := func(permission string) {
		role, err := rbac.New(fixture.db).CreateRole(ctx, "mods-"+strings.ToLower(strings.ReplaceAll(permission, ".", "-")), "")
		if err != nil {
			t.Fatal(err)
		}
		if err = rbac.New(fixture.db).ReplacePermissions(ctx, role.ID, []string{permission}); err != nil {
			t.Fatal(err)
		}
		if err = rbac.New(fixture.db).AssignUser(ctx, member.ID, role.ID, rbac.Scope{Type: "server", ID: &fixture.server}); err != nil {
			t.Fatal(err)
		}
	}
	body, contentType := multipartBody()
	if code := do(http.MethodGet, nil, "", "", false); code != http.StatusForbidden {
		t.Fatalf("list without Files.View: %d", code)
	}
	grant("Files.View")
	if code := do(http.MethodGet, nil, "", "", false); code != http.StatusOK {
		t.Fatalf("list with Files.View: %d", code)
	}
	if code := do(http.MethodPost, body, contentType, "", true); code != http.StatusForbidden {
		t.Fatalf("Files.View must not imply adding mods: %d", code)
	}
	grant("Files.Upload")
	if code := do(http.MethodPost, body, contentType, "", false); code != http.StatusForbidden && code != http.StatusBadRequest {
		t.Fatalf("add without CSRF must be refused: %d", code)
	}
	if code := do(http.MethodPost, body, contentType, "", true); code != http.StatusCreated {
		t.Fatalf("add with Files.Upload: %d", code)
	}
	if code := do(http.MethodDelete, nil, "", "?file=a.jar", true); code != http.StatusForbidden {
		t.Fatalf("Files.Upload must not imply removing mods: %d", code)
	}
	grant("Files.Delete")
	if code := do(http.MethodDelete, nil, "", "?file=a.jar", true); code != http.StatusNoContent {
		t.Fatalf("remove with Files.Delete: %d", code)
	}
}

func TestMinecraftVersionsEndpointGuards(t *testing.T) {
	fixture := newFilesFixture(t)
	if response := fixture.request(http.MethodGet, "/api/v1/minecraft/versions?loader=fabric", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", response.Code)
	}
	if response := fixture.request(http.MethodGet, "/api/v1/minecraft/versions?loader=quilt", true); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown loader: %d", response.Code)
	}
	if response := fixture.request(http.MethodGet, "/api/v1/minecraft/versions?loader=http://evil.example", true); response.Code != http.StatusBadRequest {
		t.Fatalf("URL as loader: %d", response.Code)
	}
}

func TestModsAPIDisableEnableNeedsRenamePermission(t *testing.T) {
	fixture := newFilesFixture(t)
	if response := fixture.uploadMod("sodium.jar", modJar(t, "sodium", "Sodium"), false); response.Code != http.StatusCreated {
		t.Fatalf("add: %d", response.Code)
	}
	patch := func(file string, enabled any) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(map[string]any{"file": file, "enabled": enabled})
		request := httptest.NewRequest(http.MethodPatch, "/api/v1/servers/"+fixture.server+"/mods", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", fixture.csrf)
		request.AddCookie(fixture.cookie)
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		return recorder
	}
	response := patch("sodium.jar", false)
	var mod struct {
		FileName string `json:"file_name"`
		Disabled bool   `json:"disabled"`
		ID       string `json:"id"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &mod) != nil || mod.FileName != "sodium.jar.disabled" || !mod.Disabled || mod.ID != "sodium" {
		t.Fatalf("disable: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "mods", "sodium.jar.disabled")); err != nil {
		t.Fatal("disabled file missing")
	}
	listed := fixture.request(http.MethodGet, "/api/v1/servers/"+fixture.server+"/mods", true)
	if !strings.Contains(listed.Body.String(), `"disabled":true`) {
		t.Fatalf("list must flag the disabled mod: %s", listed.Body.String())
	}
	for name, recorder := range map[string]*httptest.ResponseRecorder{"traversal": patch("../x.jar", true), "missing flag": patch("sodium.jar.disabled", nil), "wrong type": patch("sodium.jar.disabled", "yes")} {
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, recorder.Code)
		}
	}
	if recorder := patch("gone.jar", false); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing mod: %d", recorder.Code)
	}
	if response = patch("sodium.jar.disabled", true); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"file_name":"sodium.jar"`) {
		t.Fatalf("enable: %d %s", response.Code, response.Body.String())
	}
}
