package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	"gamenode/internal/templates"
)

type testSession struct {
	cookie *http.Cookie
	csrf   string
}

func createAdminSession(t *testing.T, h http.Handler) testSession {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	if json.NewDecoder(response.Body).Decode(&body) != nil {
		t.Fatal("decode setup")
	}
	return testSession{response.Result().Cookies()[0], body.CSRF}
}
func loginSession(t *testing.T, h http.Handler, username string) testSession {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"`+username+`","password":"a password long enough"}`))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	json.NewDecoder(response.Body).Decode(&body)
	return testSession{response.Result().Cookies()[0], body.CSRF}
}
func eggEnvelope(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../templates/testdata/7-days-to-die.json")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]json.RawMessage{"egg": data})
	return encoded
}
func templateRequest(h http.Handler, method, path string, body []byte, session *testSession, csrf bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if session != nil {
		request.AddCookie(session.cookie)
		if csrf {
			request.Header.Set("X-CSRF-Token", session.csrf)
		}
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestTemplateImportPreviewPersistenceDeleteAndAudit(t *testing.T) {
	h, db := newTestServer(t)
	if response := templateRequest(h, http.MethodGet, "/api/v1/templates", nil, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d", response.Code)
	}
	admin := createAdminSession(t, h)
	if response := templateRequest(h, http.MethodPost, "/api/v1/templates/analyze/egg", eggEnvelope(t), &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("preview without csrf=%d", response.Code)
	}
	preview := templateRequest(h, http.MethodPost, "/api/v1/templates/analyze/egg", eggEnvelope(t), &admin, true)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview=%d %s", preview.Code, preview.Body.String())
	}
	var analyzed templates.Template
	if json.NewDecoder(preview.Body).Decode(&analyzed) != nil || analyzed.Installer.SteamCMD.AppID != 294420 || analyzed.Compatibility.Status != templates.PartiallyCompatible {
		t.Fatalf("preview=%#v", analyzed)
	}
	importedResponse := templateRequest(h, http.MethodPost, "/api/v1/templates/import/egg", eggEnvelope(t), &admin, true)
	if importedResponse.Code != http.StatusCreated {
		t.Fatalf("import=%d %s", importedResponse.Code, importedResponse.Body.String())
	}
	var imported templates.Template
	json.NewDecoder(importedResponse.Body).Decode(&imported)
	listed := templateRequest(h, http.MethodGet, "/api/v1/templates", nil, &admin, false)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), imported.ID) {
		t.Fatalf("list=%d %s", listed.Code, listed.Body.String())
	}
	deleted := templateRequest(h, http.MethodDelete, "/api/v1/templates/"+imported.ID, nil, &admin, true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d", deleted.Code)
	}
	var imports, deletes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='template.import' AND result='success'`).Scan(&imports); err != nil || imports != 1 {
		t.Fatalf("imports=%d err=%v", imports, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='template.delete' AND result='success'`).Scan(&deletes); err != nil || deletes != 1 {
		t.Fatalf("deletes=%d err=%v", deletes, err)
	}
}

func TestTemplatePermissionsAreIndependentAndGlobalOnly(t *testing.T) {
	h, db := newTestServer(t)
	createAdminSession(t, h)
	ctx := context.Background()
	identities := identity.New(db)
	authorization := rbac.New(db)
	makeUser := func(name, permission, scope string) testSession {
		user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: name, Email: name + "@example.test", Password: "a password long enough"})
		if err != nil {
			t.Fatal(err)
		}
		role, err := authorization.CreateRole(ctx, name+"-role", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = authorization.ReplacePermissions(ctx, role.ID, []string{permission}); err != nil {
			t.Fatal(err)
		}
		assignment := rbac.Scope{Type: scope}
		if scope == "server" {
			if _, err = db.Exec(`INSERT INTO servers(id,name,description,creation_mode,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, name+"-server", "server", "", "custom", "C:/", "test.exe", "[]", "{}", "native", 0, "never", "terminate", "", 15, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
				t.Fatal(err)
			}
			id := name + "-server"
			assignment.ID = &id
		}
		if err = authorization.AssignUser(ctx, user.ID, role.ID, assignment); err != nil {
			t.Fatal(err)
		}
		return loginSession(t, h, name)
	}
	manager := makeUser("manager", "Templates.Manage", "global")
	if response := templateRequest(h, http.MethodPost, "/api/v1/templates/analyze/egg", eggEnvelope(t), &manager, true); response.Code != http.StatusOK {
		t.Fatalf("manager preview=%d", response.Code)
	}
	if response := templateRequest(h, http.MethodGet, "/api/v1/templates", nil, &manager, false); response.Code != http.StatusForbidden {
		t.Fatalf("manage implied view=%d", response.Code)
	}
	viewer := makeUser("viewer", "Templates.View", "global")
	if response := templateRequest(h, http.MethodGet, "/api/v1/templates", nil, &viewer, false); response.Code != http.StatusOK {
		t.Fatalf("viewer list=%d", response.Code)
	}
	if response := templateRequest(h, http.MethodPost, "/api/v1/templates/analyze/egg", eggEnvelope(t), &viewer, true); response.Code != http.StatusForbidden {
		t.Fatalf("view implied manage=%d", response.Code)
	}
	serverScoped := makeUser("scoped", "Templates.View", "server")
	if response := templateRequest(h, http.MethodGet, "/api/v1/templates", nil, &serverScoped, false); response.Code != http.StatusForbidden {
		t.Fatalf("server scoped templates view=%d", response.Code)
	}
}

func TestTemplateImportErrorsAreBoundedAndSanitized(t *testing.T) {
	h, db := newTestServer(t)
	admin := createAdminSession(t, h)
	secret := "TOP_SECRET_DO_NOT_AUDIT"
	invalid := []byte(`{"egg":{"name":"x","startup":"./server","variables":[{"env_variable":"DUP","default_value":"` + secret + `"},{"env_variable":"dup"}]}}`)
	response := templateRequest(h, http.MethodPost, "/api/v1/templates/import/egg", invalid, &admin, true)
	if response.Code != http.StatusUnprocessableEntity || strings.Contains(response.Body.String(), secret) {
		t.Fatalf("invalid response=%d %s", response.Code, response.Body.String())
	}
	oversized := []byte(`{"egg":"` + strings.Repeat("x", templates.MaxEggBytes) + `"}`)
	response = templateRequest(h, http.MethodPost, "/api/v1/templates/import/egg", oversized, &admin, true)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized=%d", response.Code)
	}
	var auditText string
	if err := db.QueryRow(`SELECT COALESCE(resource_name,'')||COALESCE(metadata_json,'')||COALESCE(error_summary,'') FROM audit_log WHERE action='template.import' AND result='failure'`).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditText, secret) || strings.Contains(auditText, "$(echo") {
		t.Fatalf("secret leaked to audit: %q", auditText)
	}
}

func TestTemplateAuditFailureDoesNotChangeImportResult(t *testing.T) {
	h, db := newTestServer(t)
	admin := createAdminSession(t, h)
	if _, err := db.Exec(`CREATE TRIGGER fail_template_audit BEFORE INSERT ON audit_log WHEN NEW.action LIKE 'template.%' BEGIN SELECT RAISE(ABORT, 'forced audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	response := templateRequest(h, http.MethodPost, "/api/v1/templates/import/egg", eggEnvelope(t), &admin, true)
	if response.Code != http.StatusCreated {
		t.Fatalf("import with audit failure=%d %s", response.Code, response.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM game_templates`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("persisted templates=%d err=%v", count, err)
	}
}
