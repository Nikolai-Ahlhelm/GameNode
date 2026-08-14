package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"gamenode/internal/ports"
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

func TestSettingsReadWriteAndAudit(t *testing.T) {
	h, db := newTestServer(t)
	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	h.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settings read: %d", unauthenticatedResponse.Code)
	}

	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.NewDecoder(setupResponse.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	cookie := setupResponse.Result().Cookies()[0]

	read := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	read.AddCookie(cookie)
	readResponse := httptest.NewRecorder()
	h.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("settings read: %d %s", readResponse.Code, readResponse.Body.String())
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"monitoring":{"sample_interval_seconds":7}}`))
	patch.AddCookie(cookie)
	patch.Header.Set("X-CSRF-Token", session.CSRF)
	patchResponse := httptest.NewRecorder()
	h.ServeHTTP(patchResponse, patch)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("settings patch: %d %s", patchResponse.Code, patchResponse.Body.String())
	}
	events, err := audit.New(db).List(context.Background(), audit.Filter{Action: audit.SettingsUpdate})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Result != audit.Success || string(events[0].Metadata) != `{"changed_fields":["monitoring.sample_interval_seconds"]}` {
		t.Fatalf("unexpected settings audit events: %#v", events)
	}
}

func TestPasswordPolicyDefaultsAndAppliesImmediately(t *testing.T) {
	h, _ := newTestServer(t)
	status := httptest.NewRecorder()
	h.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil))
	if status.Code != http.StatusOK || !bytes.Contains(status.Body.Bytes(), []byte(`"password_minimum_length":8`)) || !bytes.Contains(status.Body.Bytes(), []byte(`"password_maximum_length":256`)) {
		t.Fatalf("unexpected default password policy: %d %s", status.Code, status.Body.String())
	}

	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"12345678"}`)))
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("eight-character default password rejected: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.NewDecoder(setupResponse.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	cookie := setupResponse.Result().Cookies()[0]

	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"security":{"password_minimum_length":10,"password_maximum_length":24}}`))
	patch.AddCookie(cookie)
	patch.Header.Set("X-CSRF-Token", session.CSRF)
	patchResponse := httptest.NewRecorder()
	h.ServeHTTP(patchResponse, patch)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("password policy patch: %d %s", patchResponse.Code, patchResponse.Body.String())
	}

	create := func(username, password string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewBufferString(fmt.Sprintf(`{"username":%q,"email":%q,"password":%q}`, username, username+"@example.test", password)))
		request.AddCookie(cookie)
		request.Header.Set("X-CSRF-Token", session.CSRF)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	if response := create("short", "12345678"); response.Code != http.StatusBadRequest {
		t.Fatalf("password below configured minimum accepted: %d %s", response.Code, response.Body.String())
	}
	if response := create("valid", "1234567890"); response.Code != http.StatusCreated {
		t.Fatalf("password matching configured policy rejected: %d %s", response.Code, response.Body.String())
	}
}

func TestInstanceBrandingAndFavicon(t *testing.T) {
	h, _ := newTestServer(t)
	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"12345678"}`)))
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.NewDecoder(setupResponse.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	cookie := setupResponse.Result().Cookies()[0]

	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"branding":{"name":"EU Game Host","subtitle":"Frankfurt instance"}}`))
	patch.AddCookie(cookie)
	patch.Header.Set("X-CSRF-Token", session.CSRF)
	patchResponse := httptest.NewRecorder()
	h.ServeHTTP(patchResponse, patch)
	if patchResponse.Code != http.StatusOK || !bytes.Contains(patchResponse.Body.Bytes(), []byte(`"name":"EU Game Host"`)) {
		t.Fatalf("branding patch: %d %s", patchResponse.Code, patchResponse.Body.String())
	}

	pngData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRequest(http.MethodPut, "/api/v1/settings/favicon", bytes.NewReader(pngData))
	upload.AddCookie(cookie)
	upload.Header.Set("X-CSRF-Token", session.CSRF)
	uploadResponse := httptest.NewRecorder()
	h.ServeHTTP(uploadResponse, upload)
	if uploadResponse.Code != http.StatusOK || !bytes.Contains(uploadResponse.Body.Bytes(), []byte(`"custom_favicon":true`)) {
		t.Fatalf("favicon upload: %d %s", uploadResponse.Code, uploadResponse.Body.String())
	}

	faviconResponse := httptest.NewRecorder()
	h.ServeHTTP(faviconResponse, httptest.NewRequest(http.MethodGet, "/api/v1/branding/favicon", nil))
	if faviconResponse.Code != http.StatusOK || faviconResponse.Header().Get("Content-Type") != "image/png" || !bytes.Equal(faviconResponse.Body.Bytes(), pngData) {
		t.Fatalf("favicon get: %d %s", faviconResponse.Code, faviconResponse.Header().Get("Content-Type"))
	}

	remove := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/favicon", nil)
	remove.AddCookie(cookie)
	remove.Header.Set("X-CSRF-Token", session.CSRF)
	removeResponse := httptest.NewRecorder()
	h.ServeHTTP(removeResponse, remove)
	if removeResponse.Code != http.StatusOK || !bytes.Contains(removeResponse.Body.Bytes(), []byte(`"custom_favicon":false`)) {
		t.Fatalf("favicon delete: %d %s", removeResponse.Code, removeResponse.Body.String())
	}
}

func TestAuthAuditLoginAndLogout(t *testing.T) {
	h, db := newTestServer(t)
	const password = "AUDIT_PASSWORD_SHOULD_NEVER_APPEAR"
	const sessionSecret = "AUDIT_SESSION_SHOULD_NEVER_APPEAR"
	const csrfSecret = "AUDIT_CSRF_SHOULD_NEVER_APPEAR"

	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"`+password+`"}`))
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setupResponse.Code, setupResponse.Body.String())
	}

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"`+password+`"}`))
	login.Header.Set("Content-Type", "application/json")
	login.RemoteAddr = "198.51.100.7:4312"
	login.Header.Set("X-Forwarded-For", "203.0.113.9")
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginBody struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	if loginBody.CSRFToken == "" {
		t.Fatal("login response did not contain CSRF token")
	}

	failedLogin := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"wrong"}`))
	failedLogin.Header.Set("Content-Type", "application/json")
	failedLogin.RemoteAddr = "198.51.100.8:4312"
	failedLoginResponse := httptest.NewRecorder()
	h.ServeHTTP(failedLoginResponse, failedLogin)
	if failedLoginResponse.Code != http.StatusUnauthorized {
		t.Fatalf("failed login: %d", failedLoginResponse.Code)
	}

	logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.RemoteAddr = "198.51.100.7:4312"
	logout.AddCookie(loginResponse.Result().Cookies()[0])
	logout.Header.Set("X-CSRF-Token", loginBody.CSRFToken)
	logoutResponse := httptest.NewRecorder()
	h.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", logoutResponse.Code, logoutResponse.Body.String())
	}

	var adminID string
	if err := db.QueryRow(`SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT action, result, actor_user_id, actor_username, remote_ip, resource_name, metadata_json, error_summary FROM audit_log ORDER BY timestamp DESC, id DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type auditRow struct {
		action, result, actorID, username, remoteIP, resourceName, metadata, summary sql.NullString
	}
	var events []auditRow
	for rows.Next() {
		var event auditRow
		if err := rows.Scan(&event.action, &event.result, &event.actorID, &event.username, &event.remoteIP, &event.resourceName, &event.metadata, &event.summary); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("audit events = %d, want exactly 3", len(events))
	}
	for _, event := range events {
		serialized := event.action.String + event.result.String + event.actorID.String + event.username.String + event.remoteIP.String + event.resourceName.String + event.metadata.String + event.summary.String
		for _, secret := range []string{password, sessionSecret, csrfSecret} {
			if strings.Contains(serialized, secret) {
				t.Fatalf("audit event contains secret %q", secret)
			}
		}
	}

	var loginSuccess, loginFailure, logoutSuccess *auditRow
	for index := range events {
		event := &events[index]
		switch event.action.String + ":" + event.result.String {
		case "auth.login:success":
			loginSuccess = event
		case "auth.login:failure":
			loginFailure = event
		case "auth.logout:success":
			logoutSuccess = event
		}
	}
	if loginSuccess == nil || loginSuccess.actorID.String != adminID || loginSuccess.username.String != "admin" || loginSuccess.remoteIP.String != "198.51.100.7" {
		t.Fatalf("unexpected login success audit event: %+v", loginSuccess)
	}
	if loginFailure == nil || loginFailure.actorID.Valid || loginFailure.resourceName.String != "admin" || loginFailure.remoteIP.String != "198.51.100.8" {
		t.Fatalf("unexpected login failure audit event: %+v", loginFailure)
	}
	if logoutSuccess == nil || logoutSuccess.actorID.String != adminID || logoutSuccess.username.String != "admin" || logoutSuccess.remoteIP.String != "198.51.100.7" {
		t.Fatalf("unexpected logout success audit event: %+v", logoutSuccess)
	}
}

func TestAuthAuditWriteFailureDoesNotChangeLoginResult(t *testing.T) {
	h, db := newTestServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	if _, err := db.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"a password long enough"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	h.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login changed by audit persistence failure: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
}

func TestServerAndPortAuditMutations(t *testing.T) {
	h, db := newTestServer(t)
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
	cookie := setupResponse.Result().Cookies()[0]
	request := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", session.CSRFToken)
		r.RemoteAddr = "198.51.100.11:4312"
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	createBody := `{"creation_mode":"adopt","name":"audit server","working_directory":"` + filepath.ToSlash(filepath.Dir(executable)) + `","executable":"` + filepath.ToSlash(executable) + `","arguments":["AUDIT_ARG_SECRET_SHOULD_NEVER_APPEAR"],"environment_variables":{"AUDIT_SECRET":"AUDIT_ENV_SECRET_SHOULD_NEVER_APPEAR"},"stop_timeout_seconds":1}`
	createdResponse := request(http.MethodPost, "/api/v1/servers", createBody)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created servers.Record
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	updatedServer := created.Server
	updatedServer.Name = "audit server updated"
	updateBody, err := json.Marshal(updatedServer)
	if err != nil {
		t.Fatal(err)
	}
	updatedResponse := request(http.MethodPatch, "/api/v1/servers/"+created.Server.ID, string(updateBody))
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updatedResponse.Code, updatedResponse.Body.String())
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	portResponse := request(http.MethodPost, "/api/v1/servers/"+created.Server.ID+"/ports", `{"name":"Game","protocol":"tcp","bind_address":"127.0.0.1","port":`+strconv.Itoa(port)+`}`)
	if portResponse.Code != http.StatusCreated {
		t.Fatalf("port create: %d %s", portResponse.Code, portResponse.Body.String())
	}
	var createdPort struct {
		Port ports.Port `json:"port"`
	}
	if err := json.Unmarshal(portResponse.Body.Bytes(), &createdPort); err != nil {
		t.Fatal(err)
	}
	portConflictResponse := request(http.MethodPost, "/api/v1/servers/"+created.Server.ID+"/ports", `{"name":"Conflict","protocol":"tcp","bind_address":"127.0.0.1","port":`+strconv.Itoa(port)+`}`)
	if portConflictResponse.Code != http.StatusConflict {
		t.Fatalf("port collision: %d %s", portConflictResponse.Code, portConflictResponse.Body.String())
	}
	if strings.Contains(portConflictResponse.Body.String(), "another GameNode server") {
		t.Fatalf("port collision exposed an internal error: %s", portConflictResponse.Body.String())
	}
	portUpdateResponse := request(http.MethodPatch, "/api/v1/servers/"+created.Server.ID+"/ports/"+createdPort.Port.ID, `{"name":"Query","protocol":"udp","bind_address":"127.0.0.1","port":`+strconv.Itoa(port)+`}`)
	if portUpdateResponse.Code != http.StatusOK {
		t.Fatalf("port update: %d %s", portUpdateResponse.Code, portUpdateResponse.Body.String())
	}
	portDeleteResponse := request(http.MethodDelete, "/api/v1/servers/"+created.Server.ID+"/ports/"+createdPort.Port.ID, "")
	if portDeleteResponse.Code != http.StatusNoContent {
		t.Fatalf("port delete: %d %s", portDeleteResponse.Code, portDeleteResponse.Body.String())
	}
	deleteResponse := request(http.MethodDelete, "/api/v1/servers/"+created.Server.ID, "")
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}

	for _, action := range []string{"server.create", "server.update", "server.delete", "port.create", "port.update", "port.delete"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action=? AND result='success'`, action).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s success events = %d, want 1", action, count)
		}
	}
	var portFailureCount int
	if err := db.QueryRow(`SELECT count(*) FROM audit_log WHERE action='port.create' AND result='failure' AND error_code='port_conflict'`).Scan(&portFailureCount); err != nil {
		t.Fatal(err)
	}
	if portFailureCount != 1 {
		t.Fatalf("port collision audit events = %d, want 1", portFailureCount)
	}
	var actorID, serverID, metadata, resourceName string
	if err := db.QueryRow(`SELECT actor_user_id,server_id,COALESCE(metadata_json,''),resource_name FROM audit_log WHERE action='port.update' AND result='success'`).Scan(&actorID, &serverID, &metadata, &resourceName); err != nil {
		t.Fatal(err)
	}
	if actorID == "" || serverID != created.Server.ID || resourceName != "Query" || !strings.Contains(metadata, `"protocol":"udp"`) {
		t.Fatalf("unexpected port audit event: actor=%q server=%q name=%q metadata=%q", actorID, serverID, resourceName, metadata)
	}
	rows, err := db.Query(`SELECT COALESCE(metadata_json,''),COALESCE(resource_name,''),COALESCE(error_summary,'') FROM audit_log WHERE action LIKE 'server.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var metadata, name, summary string
		if err := rows.Scan(&metadata, &name, &summary); err != nil {
			t.Fatal(err)
		}
		serialized := metadata + name + summary
		for _, secret := range []string{"AUDIT_ARG_SECRET_SHOULD_NEVER_APPEAR", "AUDIT_ENV_SECRET_SHOULD_NEVER_APPEAR"} {
			if strings.Contains(serialized, secret) {
				t.Fatalf("server audit event contains secret %q", secret)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
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

func TestJSONMutationRejectsTrailingDocument(t *testing.T) {
	h, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"} {}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON document status = %d, body=%s", w.Code, w.Body.String())
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

func TestLocalReverseProxyAcceptsForwardedHTTPSOriginOnlyFromLoopback(t *testing.T) {
	newHandler := func(t *testing.T) http.Handler {
		t.Helper()
		db, err := database.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
			t.Fatal(err)
		}
		return api.New(auth.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative()), slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), true, api.Options{TrustLocalProxy: true}).Handler(http.NotFoundHandler())
	}
	request := func(remote string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8888/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
		r.Host = "127.0.0.1:8888"
		r.RemoteAddr = remote
		r.Header.Set("Origin", "https://gn.example.test")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "gn.example.test")
		return r
	}

	h := newHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("127.0.0.1:55000"))
	if w.Code != http.StatusOK {
		t.Fatalf("loopback proxy setup: %d %s", w.Code, w.Body.String())
	}
	if cookies := w.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Fatal("reverse-proxy session cookie was not marked Secure")
	}

	h = newHandler(t)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("192.0.2.25:55000"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback forwarded origin status = %d, body=%s", w.Code, w.Body.String())
	}
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

func TestIdentityAdministrationLifecycleCSRFAndAudit(t *testing.T) {
	h, db := newTestServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`))
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	h.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(setupResponse.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	cookie := setupResponse.Result().Cookies()[0]
	request := func(method, path, body string, withCSRF bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.AddCookie(cookie)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if withCSRF {
			r.Header.Set("X-CSRF-Token", session.CSRF)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	created := request(http.MethodPost, "/api/v1/users", `{"username":"member","email":"member@example.test","password":"a password long enough"}`, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", created.Code, created.Body.String())
	}
	var payload struct {
		User identity.User `json:"user"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response := request(http.MethodPatch, "/api/v1/users/"+payload.User.ID, `{"display_name":"Member One"}`, false); response.Code != http.StatusForbidden {
		t.Fatalf("patch without CSRF: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPatch, "/api/v1/users/"+payload.User.ID, `{"display_name":"Member One"}`, true); response.Code != http.StatusOK {
		t.Fatalf("edit user: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/api/v1/users/"+payload.User.ID+"/password", `{"password":"a different password long enough"}`, true); response.Code != http.StatusNoContent {
		t.Fatalf("reset password: %d %s", response.Code, response.Body.String())
	}
	for _, enabled := range []bool{false, true} {
		response := request(http.MethodPatch, "/api/v1/users/"+payload.User.ID, `{"enabled":`+strconv.FormatBool(enabled)+`}`, true)
		if response.Code != http.StatusOK {
			t.Fatalf("set enabled=%t: %d %s", enabled, response.Code, response.Body.String())
		}
	}
	if response := request(http.MethodDelete, "/api/v1/users/"+payload.User.ID, "", true); response.Code != http.StatusNoContent {
		t.Fatalf("delete user: %d %s", response.Code, response.Body.String())
	}
	var adminID string
	if err := db.QueryRow("SELECT id FROM users WHERE username='admin'").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	lastAdmin := request(http.MethodPatch, "/api/v1/users/"+adminID, `{"enabled":false}`, true)
	if lastAdmin.Code != http.StatusConflict || !strings.Contains(lastAdmin.Body.String(), "At least one active administrator must remain.") {
		t.Fatalf("last admin response: %d %s", lastAdmin.Code, lastAdmin.Body.String())
	}
	for _, action := range []string{audit.UserCreate, audit.UserUpdate, audit.UserPasswordReset, audit.UserDisable, audit.UserEnable, audit.UserDelete} {
		events, err := audit.New(db).List(context.Background(), audit.Filter{Action: action})
		if err != nil || len(events) == 0 {
			t.Fatalf("audit action %s: %v %v", action, events, err)
		}
		for _, event := range events {
			if strings.Contains(string(event.Metadata), "different password") {
				t.Fatalf("password leaked to audit metadata for %s", action)
			}
		}
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
	if response := request(http.MethodGet, "/api/v1/users/"+user.User.ID+"/groups", ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), group.Group.ID) {
		t.Fatalf("list user groups: %d %s", response.Code, response.Body.String())
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
