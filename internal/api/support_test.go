package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/support"
)

const supportBundlePath = "/api/v1/support/bundle"

type supportSession struct {
	user   auth.User
	cookie *http.Cookie
	csrf   string
}

type failingSupport struct{ err error }

func (f failingSupport) Generate(context.Context, io.Writer, support.Scope) error { return f.err }

type countingSupport struct{ calls int }

func (s *countingSupport) Generate(_ context.Context, w io.Writer, _ support.Scope) error {
	s.calls++
	z := zip.NewWriter(w)
	for _, name := range []string{"manifest.json", "diagnostics.json", "settings.json", "audit-recent.json", "servers.json"} {
		entry, err := z.Create(name)
		if err != nil {
			return err
		}
		if _, err = entry.Write([]byte("{}")); err != nil {
			return err
		}
	}
	return z.Close()
}

func newSupportServer(t *testing.T, generator any) (http.Handler, *sql.DB, *auth.Service, *servers.Service) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	authService := auth.New(db)
	serverService := servers.NewService(servers.NewStore(db), runtime.NewNative())
	options := api.Options{}
	if generator != nil {
		options.Support = generator.(interface {
			Generate(context.Context, io.Writer, support.Scope) error
		})
	}
	return api.New(authService, serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, options).Handler(http.NotFoundHandler()), db, authService, serverService
}

func supportSessionFor(t *testing.T, authService *auth.Service, user auth.User) supportSession {
	t.Helper()
	raw, csrf, err := authService.CreateSession(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return supportSession{user: user, cookie: &http.Cookie{Name: "gamenode_session", Value: raw}, csrf: csrf}
}

func supportRequest(session *supportSession, csrf string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, supportBundlePath, nil)
	if session != nil {
		r.AddCookie(session.cookie)
		r.Header.Set("X-CSRF-Token", csrf)
	}
	return r
}

func createSupportUser(t *testing.T, db *sql.DB, authService *auth.Service, username string, admin bool) supportSession {
	t.Helper()
	var user auth.User
	var err error
	if admin {
		user, err = authService.CreateInitialAdmin(context.Background(), username, username+"@example.test", "a password long enough")
	} else {
		created, createErr := identity.New(db).CreateUser(context.Background(), identity.CreateUserInput{Username: username, Email: username + "@example.test", Password: "a password long enough"})
		err = createErr
		user = auth.User{ID: created.ID, Username: created.Username}
	}
	if err != nil {
		t.Fatal(err)
	}
	return supportSessionFor(t, authService, user)
}

func grantSupportPermission(t *testing.T, db *sql.DB, userID, permission string, scope rbac.Scope) {
	t.Helper()
	service := rbac.New(db)
	role, err := service.CreateRole(context.Background(), "support-"+strings.ToLower(strings.ReplaceAll(permission, ".", "-"))+"-"+userID[:6], "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(context.Background(), role.ID, []string{permission}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(context.Background(), userID, role.ID, scope); err != nil {
		t.Fatal(err)
	}
}

func createSupportServer(t *testing.T, serverService *servers.Service) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record, err := serverService.Create(context.Background(), servers.Server{CreationMode: servers.CreationAdopt, Name: "support scope", WorkingDirectory: filepath.Dir(executable), Executable: executable, RuntimeType: "native", StopMethod: "terminate", StopTimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	return record.Server.ID
}

func assertSupportZIP(t *testing.T, body []byte) {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"manifest.json": true, "diagnostics.json": true, "settings.json": true, "audit-recent.json": true, "servers.json": true}
	if len(reader.File) != len(want) {
		t.Fatalf("ZIP entries = %d, want %d", len(reader.File), len(want))
	}
	for _, entry := range reader.File {
		if !want[entry.Name] {
			t.Fatalf("unexpected ZIP entry %q", entry.Name)
		}
	}
}

func TestSupportBundleAuthenticationAndRBAC(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		h, _, _, _ := newSupportServer(t, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, supportRequest(nil, ""))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", w.Code)
		}
	})
	for _, test := range []struct {
		name       string
		permission string
		scope      string
		want       int
		admin      bool
	}{
		{"no manage", "", "", http.StatusForbidden, false},
		{"settings view only", "Settings.View", "global", http.StatusForbidden, false},
		{"server scoped manage", "Settings.Manage", "server", http.StatusForbidden, false},
		{"global manage", "Settings.Manage", "global", http.StatusOK, false},
		{"admin", "", "", http.StatusOK, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, db, authService, serverService := newSupportServer(t, nil)
			session := createSupportUser(t, db, authService, "user"+strings.ReplaceAll(test.name, " ", ""), test.admin)
			if test.permission != "" {
				scope := rbac.Scope{Type: "global"}
				if test.scope == "server" {
					id := createSupportServer(t, serverService)
					scope = rbac.Scope{Type: "server", ID: &id}
				}
				grantSupportPermission(t, db, session.user.ID, test.permission, scope)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, supportRequest(&session, session.csrf))
			if w.Code != test.want {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestSupportBundleCSRFFAndZIPResponse(t *testing.T) {
	h, db, authService, _ := newSupportServer(t, nil)
	session := createSupportUser(t, db, authService, "manager", false)
	grantSupportPermission(t, db, session.user.ID, "Settings.Manage", rbac.Scope{Type: "global"})
	for _, csrf := range []string{"", "invalid"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, supportRequest(&session, csrf))
		if w.Code != http.StatusForbidden {
			t.Fatalf("CSRF %q status = %d", csrf, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, supportRequest(&session, session.csrf))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("success = %d, content type = %q", w.Code, w.Header().Get("Content-Type"))
	}
	disposition := w.Header().Get("Content-Disposition")
	if !strings.HasPrefix(disposition, `attachment; filename="gamenode-support-`) || !strings.HasSuffix(disposition, `.zip"`) || strings.ContainsAny(disposition, "\\/") || strings.Contains(disposition, ":") {
		t.Fatalf("unsafe Content-Disposition %q", disposition)
	}
	assertSupportZIP(t, w.Body.Bytes())
}

func TestSupportBundleGenerationFailureIsSanitizedAndAudited(t *testing.T) {
	const secret = "SUPPORT_API_RAW_ERROR_SECRET_SHOULD_NEVER_APPEAR"
	h, db, authService, _ := newSupportServer(t, failingSupport{errors.New("backend: " + secret)})
	session := createSupportUser(t, db, authService, "failureuser", false)
	grantSupportPermission(t, db, session.user.ID, "Settings.Manage", rbac.Scope{Type: "global"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, supportRequest(&session, session.csrf))
	if w.Code == http.StatusOK || w.Header().Get("Content-Type") == "application/zip" || bytes.Contains(w.Body.Bytes(), []byte(secret)) {
		t.Fatalf("unsanitized failure response: %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	events, err := audit.New(db).List(context.Background(), audit.Filter{Action: audit.SupportBundleGenerate})
	if err != nil || len(events) != 1 || events[0].Result != audit.Failure || events[0].ErrorCode != "support_bundle_failed" || events[0].ErrorSummary != "support bundle generation failed" || strings.Contains(string(events[0].Metadata)+events[0].ErrorSummary, secret) {
		t.Fatalf("failure audit = %#v, err = %v", events, err)
	}
}

func TestSupportBundleSuccessAuditExactlyOnce(t *testing.T) {
	h, db, authService, _ := newSupportServer(t, nil)
	session := createSupportUser(t, db, authService, "auditmanager", false)
	grantSupportPermission(t, db, session.user.ID, "Settings.Manage", rbac.Scope{Type: "global"})
	r := supportRequest(&session, session.csrf)
	r.RemoteAddr = "198.51.100.9:4567"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	events, err := audit.New(db).List(context.Background(), audit.Filter{Action: audit.SupportBundleGenerate})
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %#v, err = %v", events, err)
	}
	event := events[0]
	var metadata map[string]any
	if err = json.Unmarshal(event.Metadata, &metadata); err != nil || event.Result != audit.Success || event.ActorUserID == nil || *event.ActorUserID != session.user.ID || event.ActorUsername != session.user.Username || event.ResourceType != audit.System || event.RemoteIP != "198.51.100.9" || len(metadata) != 2 || metadata["bundle_schema_version"] != float64(1) || metadata["size_bytes"] == nil {
		t.Fatalf("success audit = %#v, metadata = %#v, err = %v", event, metadata, err)
	}
}

func TestSupportBundleAuditWriteFailureDoesNotRollbackDownload(t *testing.T) {
	generator := &countingSupport{}
	h, db, authService, _ := newSupportServer(t, generator)
	session := createSupportUser(t, db, authService, "besteffort", false)
	grantSupportPermission(t, db, session.user.ID, "Settings.Manage", rbac.Scope{Type: "global"})
	if _, err := db.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, supportRequest(&session, session.csrf))
	if w.Code != http.StatusOK || generator.calls != 1 {
		t.Fatalf("status = %d, generation calls = %d", w.Code, generator.calls)
	}
	assertSupportZIP(t, w.Body.Bytes())
}
