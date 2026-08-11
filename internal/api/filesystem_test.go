package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gamenode/internal/identity"
	"gamenode/internal/rbac"
)

type filesFixture struct {
	handler http.Handler
	db      *sql.DB
	cookie  *http.Cookie
	csrf    string
	server  string
	root    string
}

func newFilesFixture(t *testing.T) filesFixture {
	t.Helper()
	handler, db := newTestServer(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "server.properties"), []byte("motd=GameNode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("read me"), 0o644); err != nil {
		t.Fatal(err)
	}

	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(setupResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"creation_mode":         "custom",
		"name":                  "file fixture",
		"working_directory":     root,
		"executable":            executable,
		"arguments":             []string{},
		"environment_variables": map[string]string{},
		"stop_timeout_seconds":  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(payload))
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("X-CSRF-Token", session.CSRFToken)
	create.AddCookie(setupResponse.Result().Cookies()[0])
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", createResponse.Code, createResponse.Body.String())
	}
	var created struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return filesFixture{handler: handler, db: db, cookie: setupResponse.Result().Cookies()[0], csrf: session.CSRFToken, server: created.Server.ID, root: root}
}

func (f filesFixture) request(method, path string, authenticated bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if authenticated {
		request.AddCookie(f.cookie)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f filesFixture) mutation(method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
	payload, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if authenticated {
		request.Header.Set("X-CSRF-Token", f.csrf)
		request.AddCookie(f.cookie)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f filesFixture) upload(path, filename string, data []byte, authenticated bool) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		panic(err)
	}
	if _, err = part.Write(data); err != nil {
		panic(err)
	}
	if err = writer.Close(); err != nil {
		panic(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers/"+f.server+"/files/upload?"+url.Values{"path": {path}}.Encode(), &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if authenticated {
		request.Header.Set("X-CSRF-Token", f.csrf)
		request.AddCookie(f.cookie)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func TestFilesAPIAuthorizationAndListing(t *testing.T) {
	fixture := newFilesFixture(t)
	path := "/api/v1/servers/" + fixture.server + "/files"
	if response := fixture.request(http.MethodGet, path, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: %d", response.Code)
	}
	response := fixture.request(http.MethodGet, path, true)
	if response.Code != http.StatusOK {
		t.Fatalf("list root: %d %s", response.Code, response.Body.String())
	}
	var listed struct {
		Entries []struct {
			Name string `json:"name"`
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 2 || listed.Entries[0].Name != "config" || listed.Entries[0].Type != "directory" || listed.Entries[1].Path != "readme.txt" {
		t.Fatalf("unexpected root listing: %#v", listed.Entries)
	}
	subdirectory := fixture.request(http.MethodGet, path+"?"+url.Values{"path": {"config"}}.Encode(), true)
	if subdirectory.Code != http.StatusOK || !strings.Contains(subdirectory.Body.String(), "server.properties") {
		t.Fatalf("list subdirectory: %d %s", subdirectory.Code, subdirectory.Body.String())
	}
	unknown := fixture.request(http.MethodGet, "/api/v1/servers/missing/files", true)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown server: %d", unknown.Code)
	}
}

func TestFilesAPIContentAndSandbox(t *testing.T) {
	fixture := newFilesFixture(t)
	contentPath := "/api/v1/servers/" + fixture.server + "/files/content?" + url.Values{"path": {"config/server.properties"}}.Encode()
	response := fixture.request(http.MethodGet, contentPath, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "motd=GameNode") || !strings.Contains(response.Body.String(), `"encoding":"utf-8"`) {
		t.Fatalf("read text: %d %s", response.Code, response.Body.String())
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	traversal := "/api/v1/servers/" + fixture.server + "/files/content?" + url.Values{"path": {"../" + filepath.Base(filepath.Dir(fixture.root)) + "/" + filepath.Base(outside)}}.Encode()
	response = fixture.request(http.MethodGet, traversal, true)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "outside secret") || strings.Contains(response.Body.String(), fixture.root) {
		t.Fatalf("traversal response: %d %s", response.Code, response.Body.String())
	}

	encodedTraversal := "/api/v1/servers/" + fixture.server + "/files?path=%2e%2e%2foutside"
	response = fixture.request(http.MethodGet, encodedTraversal, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("encoded traversal: %d %s", response.Code, response.Body.String())
	}

	missing := fixture.request(http.MethodGet, "/api/v1/servers/"+fixture.server+"/files/content?path=missing.txt", true)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing content: %d", missing.Code)
	}

	large := make([]byte, 4<<20+1)
	if err := os.WriteFile(filepath.Join(fixture.root, "large.txt"), large, 0o644); err != nil {
		t.Fatal(err)
	}
	tooLarge := fixture.request(http.MethodGet, "/api/v1/servers/"+fixture.server+"/files/content?path=large.txt", true)
	if tooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large content: %d %s", tooLarge.Code, tooLarge.Body.String())
	}

}

func TestFilesAPIMutations(t *testing.T) {
	fixture := newFilesFixture(t)
	base := "/api/v1/servers/" + fixture.server + "/files"
	if response := fixture.mutation(http.MethodPost, base+"/file", map[string]string{"path": "created.txt", "content": "first"}, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: %d", response.Code)
	}
	if response := fixture.mutation(http.MethodPost, base+"/file", map[string]string{"path": "created.txt", "content": "first"}, true); response.Code != http.StatusCreated {
		t.Fatalf("create file: %d %s", response.Code, response.Body.String())
	}
	if response := fixture.mutation(http.MethodPost, base+"/file", map[string]string{"path": "created.txt", "content": "second"}, true); response.Code != http.StatusConflict {
		t.Fatalf("create conflict: %d %s", response.Code, response.Body.String())
	}
	if response := fixture.mutation(http.MethodPost, base+"/directory", map[string]string{"path": "created-dir"}, true); response.Code != http.StatusCreated {
		t.Fatalf("create directory: %d %s", response.Code, response.Body.String())
	}
	if response := fixture.mutation(http.MethodPut, base+"/content", map[string]string{"path": "created.txt", "content": "edited"}, true); response.Code != http.StatusNoContent {
		t.Fatalf("write file: %d %s", response.Code, response.Body.String())
	}
	read := fixture.request(http.MethodGet, base+"/content?path=created.txt", true)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), "edited") {
		t.Fatalf("read after edit: %d %s", read.Code, read.Body.String())
	}
	if response := fixture.mutation(http.MethodPost, base+"/move", map[string]string{"source": "created.txt", "destination": "created-dir/moved.txt"}, true); response.Code != http.StatusNoContent {
		t.Fatalf("move: %d %s", response.Code, response.Body.String())
	}
	list := fixture.request(http.MethodGet, base+"?path=created-dir", true)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "moved.txt") {
		t.Fatalf("list after move: %d %s", list.Code, list.Body.String())
	}
	if response := fixture.mutation(http.MethodPost, base+"/file", map[string]string{"path": "../outside.txt", "content": "no"}, true); response.Code != http.StatusBadRequest {
		t.Fatalf("create traversal: %d %s", response.Code, response.Body.String())
	}
	if response := fixture.mutation(http.MethodPost, "/api/v1/servers/missing/files/file", map[string]string{"path": "created.txt", "content": "no"}, true); response.Code != http.StatusNotFound {
		t.Fatalf("unknown server mutation: %d", response.Code)
	}
	if response := fixture.request(http.MethodDelete, base+"?path=created-dir", true); response.Code != http.StatusForbidden {
		t.Fatalf("delete without csrf: %d", response.Code)
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, base+"?path=created-dir&recursive=true", nil)
	deleteRequest.Header.Set("X-CSRF-Token", fixture.csrf)
	deleteRequest.AddCookie(fixture.cookie)
	deleted := httptest.NewRecorder()
	fixture.handler.ServeHTTP(deleted, deleteRequest)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("recursive delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if response := fixture.request(http.MethodGet, base+"?path=created-dir", true); response.Code != http.StatusNotFound {
		t.Fatalf("list deleted directory: %d", response.Code)
	}
}

func TestFilesAPIUploadDownloadRoundTrip(t *testing.T) {
	fixture := newFilesFixture(t)
	payload := bytes.Repeat([]byte{0, 1, 2, 3, 4, 5}, 256<<10)
	if response := fixture.upload("config", "server.bin", payload, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upload: %d", response.Code)
	}
	upload := fixture.upload("config", "server.bin", payload, true)
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", upload.Code, upload.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(fixture.root, "config", "server.bin")); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("stored upload mismatch: %d bytes err=%v", len(got), err)
	}
	if response := fixture.upload("config", "server.bin", []byte("conflict"), true); response.Code != http.StatusConflict {
		t.Fatalf("upload conflict: %d %s", response.Code, response.Body.String())
	}
	if response := fixture.upload("../outside", "outside.bin", payload, true); response.Code != http.StatusBadRequest {
		t.Fatalf("upload target escape: %d %s", response.Code, response.Body.String())
	}

	download := fixture.request(http.MethodGet, "/api/v1/servers/"+fixture.server+"/files/download?path=config/server.bin", true)
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), payload) {
		t.Fatalf("download: %d bytes=%d", download.Code, download.Body.Len())
	}
	if download.Header().Get("Content-Type") != "application/octet-stream" || download.Header().Get("Content-Length") != strconv.Itoa(len(payload)) || !strings.Contains(download.Header().Get("Content-Disposition"), "server.bin") || strings.Contains(download.Header().Get("Content-Disposition"), fixture.root) {
		t.Fatalf("unsafe download headers: %#v", download.Header())
	}
	if response := fixture.request(http.MethodGet, "/api/v1/servers/"+fixture.server+"/files/download?path=missing.bin", true); response.Code != http.StatusNotFound {
		t.Fatalf("missing download: %d", response.Code)
	}
	if response := fixture.request(http.MethodGet, "/api/v1/servers/"+fixture.server+"/files/download?path=config", true); response.Code != http.StatusBadRequest {
		t.Fatalf("directory download: %d", response.Code)
	}
	if response := fixture.request(http.MethodGet, "/api/v1/servers/"+fixture.server+"/files/download?path=../outside.bin", true); response.Code != http.StatusBadRequest {
		t.Fatalf("outside download: %d", response.Code)
	}
}

func TestFilesRBACPermissionsAreIndependentAndScoped(t *testing.T) {
	fixture := newFilesFixture(t)
	ctx := context.Background()
	users := identity.New(fixture.db)
	member, err := users.CreateUser(ctx, identity.CreateUserInput{Username: "member", Email: "member@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"member","password":"a password long enough"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	fixture.handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login: %d", loginResponse.Code)
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	cookie := loginResponse.Result().Cookies()[0]
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		var payload *bytes.Reader
		if body == nil {
			payload = bytes.NewReader(nil)
		} else {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			payload = bytes.NewReader(encoded)
		}
		r := httptest.NewRequest(method, path, payload)
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		if method != http.MethodGet {
			r.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		fixture.handler.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/servers/" + fixture.server + "/files"
	if response := request(http.MethodGet, base, nil); response.Code != http.StatusForbidden {
		t.Fatalf("Files.View absent: %d", response.Code)
	}
	grant := func(permission string) {
		role, err := rbac.New(fixture.db).CreateRole(ctx, "role-"+strings.ToLower(strings.ReplaceAll(permission, ".", "-")), "")
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
	grant("Files.View")
	if response := request(http.MethodGet, base, nil); response.Code != http.StatusOK {
		t.Fatalf("Files.View: %d", response.Code)
	}
	if response := request(http.MethodGet, base+"/download?path=readme.txt", nil); response.Code != http.StatusForbidden {
		t.Fatalf("Files.View must not imply download: %d", response.Code)
	}
	if response := request(http.MethodPost, base+"/file", map[string]string{"path": "member.txt", "content": "x"}); response.Code != http.StatusForbidden {
		t.Fatalf("Files.View must not imply edit: %d", response.Code)
	}
	grant("Files.Edit")
	if response := request(http.MethodPost, base+"/file", map[string]string{"path": "member.txt", "content": "x"}); response.Code != http.StatusCreated {
		t.Fatalf("Files.Edit: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, base+"/move", map[string]string{"source": "member.txt", "destination": "renamed.txt"}); response.Code != http.StatusForbidden {
		t.Fatalf("Files.Edit must not imply rename: %d", response.Code)
	}
	grant("Files.Rename")
	if response := request(http.MethodPost, base+"/move", map[string]string{"source": "member.txt", "destination": "renamed.txt"}); response.Code != http.StatusNoContent {
		t.Fatalf("Files.Rename: %d", response.Code)
	}
	if response := request(http.MethodDelete, base+"?path=renamed.txt", nil); response.Code != http.StatusForbidden {
		t.Fatalf("Files.Rename must not imply delete: %d", response.Code)
	}
	grant("Files.Delete")
	if response := request(http.MethodDelete, base+"?path=renamed.txt", nil); response.Code != http.StatusNoContent {
		t.Fatalf("Files.Delete: %d", response.Code)
	}
	grant("Files.Download")
	if response := request(http.MethodGet, base+"/download?path=readme.txt", nil); response.Code != http.StatusOK {
		t.Fatalf("Files.Download: %d", response.Code)
	}
}
