package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
)

func newTestServer(t *testing.T) (http.Handler, *sql.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return api.New(auth.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative()), slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), false).Handler(http.NotFoundHandler()), db
}
func TestSetupLoginAndLogout(t *testing.T) {
	h, _ := newTestServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	setup.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, setup)
	if w.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("secure session cookie missing expected attributes")
	}
	me := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	me.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, me)
	if w.Code != http.StatusOK {
		t.Fatalf("me: %d", w.Code)
	}
	badLogin := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"wrong"}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, badLogin)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", w.Code)
	}
	logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.AddCookie(cookies[0])
	logout.Header.Set("X-CSRF-Token", "invalid")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, logout)
	if w.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", w.Code)
	}
}
func TestSetupCannotBeRepeatedOrCrossOrigin(t *testing.T) {
	h, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	r.Header.Set("Origin", "https://attacker.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross origin setup: %d", w.Code)
	}
}

func TestSecureCookieIsEnabledForTLS(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	handler := api.New(auth.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative()), slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), true).Handler(http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "gamenode_session" && cookie.Secure {
			return
		}
	}
	t.Fatal("TLS session cookie was not marked Secure")
}

func TestUserManagementRequiresAdministrator(t *testing.T) {
	h, db := newTestServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d", setupResponse.Code)
	}
	_, err := identity.New(db).CreateUser(context.Background(), identity.CreateUserInput{Username: "member", Email: "member@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"member","password":"a password long enough"}`))
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login: %d", loginResponse.Code)
	}
	list := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	list.AddCookie(loginResponse.Result().Cookies()[0])
	listResponse := httptest.NewRecorder()
	h.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusForbidden {
		t.Fatalf("non-admin users list: %d", listResponse.Code)
	}
}

func TestServerCRUD(t *testing.T) {
	h, _ := newTestServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d", setupResponse.Code)
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
	body := []byte(`{"creation_mode":"adopt","name":"test application","description":"registered only","working_directory":"` + filepath.ToSlash(filepath.Dir(executable)) + `","executable":"` + filepath.ToSlash(executable) + `","arguments":[],"environment_variables":{},"stop_timeout_seconds":1}`)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(body))
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("X-CSRF-Token", session.CSRFToken)
	create.AddCookie(setupResponse.Result().Cookies()[0])
	createResponse := httptest.NewRecorder()
	h.ServeHTTP(createResponse, create)
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
	list := httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil)
	list.AddCookie(setupResponse.Result().Cookies()[0])
	listResponse := httptest.NewRecorder()
	h.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list: %d", listResponse.Code)
	}
	remove := httptest.NewRequest(http.MethodDelete, "/api/v1/servers/"+created.Server.ID, nil)
	remove.Header.Set("X-CSRF-Token", session.CSRFToken)
	remove.AddCookie(setupResponse.Result().Cookies()[0])
	removeResponse := httptest.NewRecorder()
	h.ServeHTTP(removeResponse, remove)
	if removeResponse.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", removeResponse.Code, removeResponse.Body.String())
	}
}

func TestIdentityRBACAndFilesystemMigrationSmoke(t *testing.T) {
	h, _ := newTestServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(setupResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	adminCookie := setupResponse.Result().Cookies()[0]
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if method != http.MethodGet {
			r.Header.Set("X-CSRF-Token", session.CSRFToken)
		}
		r.AddCookie(adminCookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	userResponse := request(http.MethodPost, "/api/v1/users", `{"username":"member","email":"member@example.test","password":"a password long enough"}`)
	if userResponse.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", userResponse.Code, userResponse.Body.String())
	}
	var user struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(userResponse.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	groupResponse := request(http.MethodPost, "/api/v1/groups", `{"name":"operators"}`)
	if groupResponse.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", groupResponse.Code, groupResponse.Body.String())
	}
	var group struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	if err := json.Unmarshal(groupResponse.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodPost, "/api/v1/groups/"+group.Group.ID+"/members", `{"user_id":"`+user.User.ID+`"}`); response.Code != http.StatusNoContent {
		t.Fatalf("add member: %d %s", response.Code, response.Body.String())
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	serverResponse := request(http.MethodPost, "/api/v1/servers", `{"creation_mode":"adopt","name":"integration server","working_directory":"`+filepath.ToSlash(filepath.Dir(executable))+`","executable":"`+filepath.ToSlash(executable)+`","arguments":[],"environment_variables":{},"stop_timeout_seconds":1}`)
	if serverResponse.Code != http.StatusCreated {
		t.Fatalf("create server: %d %s", serverResponse.Code, serverResponse.Body.String())
	}
	var server struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if err := json.Unmarshal(serverResponse.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}

	roleResponse := request(http.MethodPost, "/api/v1/roles", `{"name":"file-operator","description":"integration smoke"}`)
	if roleResponse.Code != http.StatusCreated {
		t.Fatalf("create role: %d %s", roleResponse.Code, roleResponse.Body.String())
	}
	var role struct {
		Role struct {
			ID string `json:"id"`
		} `json:"role"`
	}
	if err := json.Unmarshal(roleResponse.Body.Bytes(), &role); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodPut, "/api/v1/roles/"+role.Role.ID+"/permissions", `{"permissions":["Files.View"]}`); response.Code != http.StatusNoContent {
		t.Fatalf("replace role permissions: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/api/v1/users/"+user.User.ID+"/roles", `{"role_id":"`+role.Role.ID+`","scope_type":"server","scope_id":"`+server.Server.ID+`"}`); response.Code != http.StatusCreated {
		t.Fatalf("assign user role: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/api/v1/groups/"+group.Group.ID+"/roles", `{"role_id":"`+role.Role.ID+`","scope_type":"global"}`); response.Code != http.StatusCreated {
		t.Fatalf("assign group role: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/v1/servers/"+server.Server.ID+"/files", ""); response.Code != http.StatusOK {
		t.Fatalf("list files: %d %s", response.Code, response.Body.String())
	}

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"member","password":"a password long enough"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("member login: %d", loginResponse.Code)
	}
	nonAdminFiles := httptest.NewRequest(http.MethodGet, "/api/v1/servers/"+server.Server.ID+"/files", nil)
	nonAdminFiles.AddCookie(loginResponse.Result().Cookies()[0])
	nonAdminFilesResponse := httptest.NewRecorder()
	h.ServeHTTP(nonAdminFilesResponse, nonAdminFiles)
	if nonAdminFilesResponse.Code != http.StatusOK {
		t.Fatalf("group RBAC assignment did not authorize Files API: %d", nonAdminFilesResponse.Code)
	}
}
