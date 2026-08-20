package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/logging"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/settings"
)

// newLoggingTestServer wires the same settings->logging composition main.go
// performs at startup (level, categories, and detailed-error logging all
// stay live-configurable through the settings service), so these tests
// exercise the real integration rather than the logging package alone.
func newLoggingTestServer(t *testing.T) (http.Handler, *sql.DB, *logging.Manager) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logManager, log, err := logging.New(t.TempDir(), "info")
	if err != nil {
		t.Fatal(err)
	}
	settingService := settings.New(db, settings.Defaults{})
	applyLogging := func(values settings.Values) {
		_ = logManager.SetLevel(values.Logging.Level)
		_ = logManager.SetCategories(values.Logging.Categories.AsMap())
		logManager.SetDetailedErrors(values.Logging.DetailedErrors)
	}
	initial, err := settingService.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	applyLogging(initial)
	settingService.SetOnUpdate(func(values settings.Values, _ []string) { applyLogging(values) })
	handler := api.New(auth.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative()), log, false, api.Options{Settings: settingService, Logs: logManager}).Handler(http.NotFoundHandler())
	return handler, db, logManager
}

func setupSession(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"a password long enough"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", response.Code, response.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	return response.Result().Cookies()[0], session.CSRF
}

func allEntryLines(m *logging.Manager) string {
	var b strings.Builder
	for _, entry := range m.Entries() {
		b.WriteString(entry.Line)
		b.WriteString("\n")
	}
	return b.String()
}

// TestHTTPAccessLogRoutineEntriesHiddenAtInfoLevel covers requirement: routine
// HTTP access entries must not flood the default info-level log.
func TestHTTPAccessLogRoutineEntriesHiddenAtInfoLevel(t *testing.T) {
	h, _, logManager := newLoggingTestServer(t)
	cookie, _ := setupSession(t, h)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.AddCookie(cookie)
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("me: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(allEntryLines(logManager), "http request completed") {
		t.Fatalf("routine HTTP access entry leaked at info level: %s", allEntryLines(logManager))
	}
}

// TestHTTPAccessLogEntriesVisibleAtDebugLevel covers requirement: the same
// routine entries are available once an operator opts into debug.
func TestHTTPAccessLogEntriesVisibleAtDebugLevel(t *testing.T) {
	h, _, logManager := newLoggingTestServer(t)
	cookie, csrf := setupSession(t, h)
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"level":"debug"}}`))
	patch.AddCookie(cookie)
	patch.Header.Set("X-CSRF-Token", csrf)
	patchResponse := httptest.NewRecorder()
	h.ServeHTTP(patchResponse, patch)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("level patch: %d %s", patchResponse.Code, patchResponse.Body.String())
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.AddCookie(cookie)
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("me: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(allEntryLines(logManager), "http request completed") {
		t.Fatalf("routine HTTP access entry missing at debug level: %s", allEntryLines(logManager))
	}
}

func TestHTTPAccessLogUsesForwardedSourceOnlyForTrustedLocalProxy(t *testing.T) {
	newHandler := func(t *testing.T, trustLocalProxy bool) (http.Handler, *logging.Manager) {
		t.Helper()
		db, err := database.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
			t.Fatal(err)
		}
		manager, log, err := logging.New(t.TempDir(), "debug")
		if err != nil {
			t.Fatal(err)
		}
		handler := api.New(auth.New(db), servers.NewService(servers.NewStore(db), runtime.NewNative()), log, false, api.Options{Logs: manager, TrustLocalProxy: trustLocalProxy}).Handler(http.NotFoundHandler())
		return handler, manager
	}
	request := func(remote, forwarded string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", forwarded)
		return r
	}
	cases := []struct {
		name                    string
		trust                   bool
		remote, forwarded, want string
	}{
		{name: "trusted local proxy", trust: true, remote: "127.0.0.1:55000", forwarded: "198.51.100.44", want: "198.51.100.44"},
		{name: "untrusted peer", trust: true, remote: "192.0.2.24:55000", forwarded: "198.51.100.44", want: "192.0.2.24"},
		{name: "proxy trust disabled", trust: false, remote: "127.0.0.1:55000", forwarded: "198.51.100.44", want: "127.0.0.1"},
		{name: "chained value rejected", trust: true, remote: "127.0.0.1:55000", forwarded: "198.51.100.44, 127.0.0.1", want: "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, manager := newHandler(t, tc.trust)
			h.ServeHTTP(httptest.NewRecorder(), request(tc.remote, tc.forwarded))
			entries := allEntryLines(manager)
			if !strings.Contains(entries, `source_ip=`+tc.want) {
				t.Fatalf("source IP missing or wrong: %s", entries)
			}
		})
	}
}

// TestDisabledLogCategorySuppressesItsEntries and its enabled counterpart
// cover the category toggle end to end, through the settings API.
func TestDisabledLogCategorySuppressesItsEntries(t *testing.T) {
	h, _, logManager := newLoggingTestServer(t)
	cookie, csrf := setupSession(t, h)
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"level":"debug","categories":{"http":false}}}`))
	patch.AddCookie(cookie)
	patch.Header.Set("X-CSRF-Token", csrf)
	patchResponse := httptest.NewRecorder()
	h.ServeHTTP(patchResponse, patch)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("category patch: %d %s", patchResponse.Code, patchResponse.Body.String())
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.AddCookie(cookie)
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("me: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(allEntryLines(logManager), "http request completed") {
		t.Fatalf("disabled http category still emitted an entry: %s", allEntryLines(logManager))
	}
}

func TestEnabledLogCategoryEmitsItsEntries(t *testing.T) {
	h, _, logManager := newLoggingTestServer(t)
	cookie, csrf := setupSession(t, h)
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"level":"debug","categories":{"http":true}}}`))
	patch.AddCookie(cookie)
	patch.Header.Set("X-CSRF-Token", csrf)
	patchResponse := httptest.NewRecorder()
	h.ServeHTTP(patchResponse, patch)
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("category patch: %d %s", patchResponse.Code, patchResponse.Body.String())
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.AddCookie(cookie)
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("me: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(allEntryLines(logManager), "http request completed") {
		t.Fatalf("enabled http category did not emit an entry: %s", allEntryLines(logManager))
	}
}

// settingsPersistenceFailure exercises internal/settings' ErrPersistence path
// (an actual database-layer failure, not a controlled validation error) by
// dropping the app_settings table out from under the settings service - a
// real SQLite "no such table" error, while leaving the sessions/users tables
// intact so the request still authenticates normally.
func settingsPersistenceFailure(t *testing.T, h http.Handler, db *sql.DB, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	if _, err := db.Exec(`DROP TABLE app_settings`); err != nil {
		t.Fatal(err)
	}
	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"monitoring":{"sample_interval_seconds":9}}`))
	patch.AddCookie(cookie)
	patch.Header.Set("X-CSRF-Token", csrf)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, patch)
	return response
}

// TestDetailedErrorsDisabledOmitsUnderlyingError and its enabled counterpart
// cover requirement 6/7: the raw internal/database error only ever reaches
// the local application log, and only when detailed error logging is on -
// never the API response, never the audit record.
func TestDetailedErrorsDisabledOmitsUnderlyingError(t *testing.T) {
	h, db, logManager := newLoggingTestServer(t)
	cookie, csrf := setupSession(t, h)
	response := settingsPersistenceFailure(t, h, db, cookie, csrf)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected a sanitized internal error, got %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "no such table") {
		t.Fatalf("raw database error leaked into the API response: %s", response.Body.String())
	}
	if strings.Contains(allEntryLines(logManager), "underlying_error") {
		t.Fatalf("underlying_error present while detailed error logging was disabled: %s", allEntryLines(logManager))
	}
}

func TestDetailedErrorsEnabledIncludesUnderlyingErrorLocallyOnly(t *testing.T) {
	h, db, logManager := newLoggingTestServer(t)
	cookie, csrf := setupSession(t, h)
	enable := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"detailed_errors":true}}`))
	enable.AddCookie(cookie)
	enable.Header.Set("X-CSRF-Token", csrf)
	enableResponse := httptest.NewRecorder()
	h.ServeHTTP(enableResponse, enable)
	if enableResponse.Code != http.StatusOK {
		t.Fatalf("enabling detailed errors: %d %s", enableResponse.Code, enableResponse.Body.String())
	}
	response := settingsPersistenceFailure(t, h, db, cookie, csrf)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("expected a sanitized internal error, got %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "no such table") {
		t.Fatalf("raw database error leaked into the API response even though only the local log should carry it: %s", response.Body.String())
	}
	if !strings.Contains(allEntryLines(logManager), "underlying_error") || !strings.Contains(allEntryLines(logManager), "no such table") {
		t.Fatalf("underlying_error missing from the local log while detailed error logging was enabled: %s", allEntryLines(logManager))
	}
}

// TestAuditRecordsStaySanitizedRegardlessOfDetailedErrorLogging covers
// requirement 7: audit metadata/error summaries never carry the raw error,
// whether or not detailed application-log error logging is enabled.
func TestAuditRecordsStaySanitizedRegardlessOfDetailedErrorLogging(t *testing.T) {
	h, db, _ := newLoggingTestServer(t)
	cookie, csrf := setupSession(t, h)
	enable := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"detailed_errors":true}}`))
	enable.AddCookie(cookie)
	enable.Header.Set("X-CSRF-Token", csrf)
	enableResponse := httptest.NewRecorder()
	h.ServeHTTP(enableResponse, enable)
	if enableResponse.Code != http.StatusOK {
		t.Fatalf("enabling detailed errors: %d %s", enableResponse.Code, enableResponse.Body.String())
	}
	if response := settingsPersistenceFailure(t, h, db, cookie, csrf); response.Code != http.StatusInternalServerError {
		t.Fatalf("expected a sanitized internal error, got %d %s", response.Code, response.Body.String())
	}
	// The audit table is unaffected by dropping app_settings, so the failed
	// settings.update audit event is expected to persist normally - with a
	// sanitized summary, never the raw "no such table: app_settings" text.
	events, err := audit.New(db).List(context.Background(), audit.Filter{Action: audit.SettingsUpdate, Result: audit.Failure})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly one failed settings.update audit event: %#v", events)
	}
	if strings.Contains(events[0].ErrorSummary, "no such table") || strings.Contains(string(events[0].Metadata), "no such table") {
		t.Fatalf("raw database error leaked into the audit record: %+v", events[0])
	}
}

// TestSecretsStayRedactedWithDetailedErrorLoggingEnabled covers requirement:
// enabling detailed error logging must never surface request credentials.
func TestSecretsStayRedactedWithDetailedErrorLoggingEnabled(t *testing.T) {
	h, _, logManager := newLoggingTestServer(t)
	cookie, csrf := setupSession(t, h)
	enable := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"level":"debug","detailed_errors":true}}`))
	enable.AddCookie(cookie)
	enable.Header.Set("X-CSRF-Token", csrf)
	enableResponse := httptest.NewRecorder()
	h.ServeHTTP(enableResponse, enable)
	if enableResponse.Code != http.StatusOK {
		t.Fatalf("enabling detailed errors: %d %s", enableResponse.Code, enableResponse.Body.String())
	}
	const secretPassword = "super-secret-password-value"
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"`+secretPassword+`"}`))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, login)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected the wrong password to be rejected: %d", response.Code)
	}
	if strings.Contains(allEntryLines(logManager), secretPassword) {
		t.Fatalf("submitted password leaked into the application log: %s", allEntryLines(logManager))
	}
}

// TestSettingsLoggingFieldsRequireAuthCSRFAndPermission covers requirement:
// the new logging settings ride the existing RBAC/CSRF/persistence contract,
// not a separate one.
func TestSettingsLoggingFieldsRequireAuthCSRFAndPermission(t *testing.T) {
	h, _, _ := newLoggingTestServer(t)
	unauthenticated := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"level":"debug"}}`))
	unauthenticatedResponse := httptest.NewRecorder()
	h.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated logging patch: %d", unauthenticatedResponse.Code)
	}
	cookie, csrf := setupSession(t, h)
	noCSRF := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"level":"debug"}}`))
	noCSRF.AddCookie(cookie)
	noCSRFResponse := httptest.NewRecorder()
	h.ServeHTTP(noCSRFResponse, noCSRF)
	if noCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("logging patch without CSRF token: %d", noCSRFResponse.Code)
	}
	unknownField := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"categories":{"made_up_category":false}}}`))
	unknownField.AddCookie(cookie)
	unknownField.Header.Set("X-CSRF-Token", csrf)
	unknownFieldResponse := httptest.NewRecorder()
	h.ServeHTTP(unknownFieldResponse, unknownField)
	if unknownFieldResponse.Code != http.StatusBadRequest {
		t.Fatalf("unknown logging category field was not rejected by the whitelist: %d %s", unknownFieldResponse.Code, unknownFieldResponse.Body.String())
	}
	valid := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", bytes.NewBufferString(`{"logging":{"level":"debug","categories":{"http":false},"detailed_errors":true}}`))
	valid.AddCookie(cookie)
	valid.Header.Set("X-CSRF-Token", csrf)
	validResponse := httptest.NewRecorder()
	h.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusOK {
		t.Fatalf("valid logging patch: %d %s", validResponse.Code, validResponse.Body.String())
	}
	var body struct {
		Logging struct {
			Level          string `json:"level"`
			DetailedErrors bool   `json:"detailed_errors"`
			Categories     struct {
				HTTP bool `json:"http"`
			} `json:"categories"`
		} `json:"logging"`
	}
	if err := json.NewDecoder(validResponse.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Logging.Level != "debug" || body.Logging.Categories.HTTP || !body.Logging.DetailedErrors {
		t.Fatalf("logging patch did not persist as requested: %+v", body.Logging)
	}
}
