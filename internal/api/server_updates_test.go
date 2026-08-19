package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/serverupdates"
	"gamenode/internal/steamcmd"
)

type updateAPIInstaller struct {
	block chan struct{}
	fail  bool
}

func (i *updateAPIInstaller) Install(ctx context.Context, root string, _ steamcmd.InstallPlan, _ io.Writer, sink steamcmd.EventSink) error {
	if sink != nil {
		sink(steamcmd.Event{Phase: "installing", Summary: "Updating game files"})
	}
	if i.block != nil {
		select {
		case <-i.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if i.fail {
		return context.DeadlineExceeded
	}
	return nil
}

type serverUpdateAPIFixture struct {
	handler       http.Handler
	db            *sql.DB
	serverService *servers.Service
	updater       *serverupdates.Service
	eligibleID    string
	ineligibleID  string
}

func newServerUpdateAPI(t *testing.T, installer *updateAPIInstaller) serverUpdateAPIFixture {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	updater := serverupdates.New(db, serverService, installer)
	t.Cleanup(updater.Close)

	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "server.bin"), []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	eligible, err := serverService.CreateProvisioned(context.Background(), servers.Server{Name: "Eligible", CreationMode: servers.CreationTemplate, WorkingDirectory: root, Executable: "server.bin", RuntimeType: "native", StopTimeoutSeconds: 1}, "project-zomboid", nil, nil, nil, &servers.ProvisionedSteamCMD{InstallerType: "steamcmd", AppID: 380870, LoginMode: "anonymous", Validate: true, TemplateID: "project-zomboid", TemplateVersion: "2.0.0", TemplateSource: "official"})
	if err != nil {
		t.Fatal(err)
	}
	customRoot := t.TempDir()
	if err = os.WriteFile(filepath.Join(customRoot, "server.bin"), []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	ineligible, err := serverService.Create(context.Background(), servers.Server{Name: "Custom", CreationMode: servers.CreationCustom, WorkingDirectory: customRoot, Executable: "server.bin", RuntimeType: "native", StopTimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}

	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{ServerUpdates: updater}).Handler(http.NotFoundHandler())
	return serverUpdateAPIFixture{handler, db, serverService, updater, eligible.Server.ID, ineligible.Server.ID}
}

func waitServerUpdateJob(t *testing.T, fixture serverUpdateAPIFixture, session testSession, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/server-update-jobs/"+id, nil, &session, false)
		if response.Code != http.StatusOK {
			t.Fatalf("job=%d %s", response.Code, response.Body.String())
		}
		var job map[string]any
		json.NewDecoder(response.Body).Decode(&job)
		status, _ := job["status"].(string)
		if status == "completed" || status == "failed" || status == "cancelled" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not reach a terminal state")
	return nil
}

func grantServerUpdate(t *testing.T, db *sql.DB, handler http.Handler, username, serverID string) testSession {
	t.Helper()
	ctx := context.Background()
	identities := identity.New(db)
	authorization := rbac.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: username, Email: username + "@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := authorization.CreateRole(ctx, username+"-role", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, role.ID, []string{"Server.Update"}); err != nil {
		t.Fatal(err)
	}
	scope := rbac.Scope{Type: "server", ID: &serverID}
	if serverID == "" {
		scope = rbac.Scope{Type: "global"}
	}
	if err = authorization.AssignUser(ctx, user.ID, role.ID, scope); err != nil {
		t.Fatal(err)
	}
	return loginSession(t, handler, username)
}

func TestServerUpdateStatusRequiresAuthAndPermission(t *testing.T) {
	fixture := newServerUpdateAPI(t, &updateAPIInstaller{})
	path := "/api/v1/servers/" + fixture.eligibleID + "/update"
	if response := templateRequest(fixture.handler, http.MethodGet, path, nil, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated=%d", response.Code)
	}
	admin := createAdminSession(t, fixture.handler)
	if response := templateRequest(fixture.handler, http.MethodGet, path, nil, &admin, false); response.Code != http.StatusOK {
		t.Fatalf("admin status=%d %s", response.Code, response.Body.String())
	}
	viewer := grantServerUpdate(t, fixture.db, fixture.handler, "viewer-only", "")
	_ = viewer
}

func TestServerUpdateEligibleServerShowsSafeReview(t *testing.T) {
	fixture := newServerUpdateAPI(t, &updateAPIInstaller{})
	admin := createAdminSession(t, fixture.handler)
	response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/"+fixture.eligibleID+"/update", nil, &admin, false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"eligible":true`) || !strings.Contains(body, `"app_id":380870`) || !strings.Contains(body, `"template_id":"project-zomboid"`) {
		t.Fatalf("unexpected eligibility body: %s", body)
	}
	// Never expose the server root, executable path, or a command line.
	if strings.Contains(body, "server.bin") || strings.Contains(body, "TempDir") {
		t.Fatalf("eligibility response leaked filesystem information: %s", body)
	}
}

func TestServerUpdateIneligibleServerReportsReason(t *testing.T) {
	fixture := newServerUpdateAPI(t, &updateAPIInstaller{})
	admin := createAdminSession(t, fixture.handler)
	response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/"+fixture.ineligibleID+"/update", nil, &admin, false)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"eligible":false`) {
		t.Fatalf("expected ineligible response, got %s", response.Body.String())
	}
}

func TestServerUpdateStartRequiresCSRFAndCompletesWithAudit(t *testing.T) {
	fixture := newServerUpdateAPI(t, &updateAPIInstaller{})
	admin := createAdminSession(t, fixture.handler)
	path := "/api/v1/servers/" + fixture.eligibleID + "/update"
	if response := templateRequest(fixture.handler, http.MethodPost, path, nil, &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("csrf=%d", response.Code)
	}
	response := templateRequest(fixture.handler, http.MethodPost, path, nil, &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start=%d %s", response.Code, response.Body.String())
	}
	var job map[string]any
	json.NewDecoder(response.Body).Decode(&job)
	completed := waitServerUpdateJob(t, fixture, admin, job["id"].(string))
	if completed["status"] != "completed" {
		t.Fatalf("job=%#v", completed)
	}
	var actions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('server.steamcmd_update_start','server.steamcmd_update_complete')`).Scan(&actions); err != nil || actions != 2 {
		t.Fatalf("audit actions=%d err=%v", actions, err)
	}
}

func TestServerUpdateRunningServerRejected(t *testing.T) {
	fixture := newServerUpdateAPI(t, &updateAPIInstaller{})
	if _, err := fixture.db.Exec(`UPDATE server_runtime_state SET current_state='running' WHERE server_id=?`, fixture.eligibleID); err != nil {
		t.Fatal(err)
	}
	admin := createAdminSession(t, fixture.handler)
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/servers/"+fixture.eligibleID+"/update", nil, &admin, true)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "server_not_stopped") {
		t.Fatalf("status=%d %s", response.Code, response.Body.String())
	}
}

func TestServerUpdateRequiresServerUpdatePermissionNotEditOrTemplatesManage(t *testing.T) {
	fixture := newServerUpdateAPI(t, &updateAPIInstaller{})
	createAdminSession(t, fixture.handler)
	ctx := context.Background()
	identities := identity.New(fixture.db)
	authorization := rbac.New(fixture.db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "editor", Email: "editor@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	editRole, err := authorization.CreateRole(ctx, "editor-role", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, editRole.ID, []string{"Server.Edit"}); err != nil {
		t.Fatal(err)
	}
	serverID := fixture.eligibleID
	if err = authorization.AssignUser(ctx, user.ID, editRole.ID, rbac.Scope{Type: "server", ID: &serverID}); err != nil {
		t.Fatal(err)
	}
	// Templates.Manage is global-only (catalog administration), so it is
	// granted globally here - the point is that neither it nor Server.Edit,
	// alone or combined, implicitly grants Server.Update.
	manageRole, err := authorization.CreateRole(ctx, "manager-role", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = authorization.ReplacePermissions(ctx, manageRole.ID, []string{"Templates.Manage"}); err != nil {
		t.Fatal(err)
	}
	if err = authorization.AssignUser(ctx, user.ID, manageRole.ID, rbac.Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
	session := loginSession(t, fixture.handler, "editor")
	response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/"+fixture.eligibleID+"/update", nil, &session, false)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected Server.Edit+Templates.Manage to be insufficient, got %d %s", response.Code, response.Body.String())
	}
}

func TestServerUpdateJobEndpointRequiresAuthBeforeLookup(t *testing.T) {
	fixture := newServerUpdateAPI(t, &updateAPIInstaller{})
	if response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/server-update-jobs/does-not-exist", nil, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated job read to be rejected before any lookup, got %d", response.Code)
	}
}

func TestServerUpdateJobOwnershipAndCancel(t *testing.T) {
	block := make(chan struct{})
	fixture := newServerUpdateAPI(t, &updateAPIInstaller{block: block})
	owner := grantServerUpdate(t, fixture.db, fixture.handler, "owner", fixture.eligibleID)
	other := grantServerUpdate(t, fixture.db, fixture.handler, "other", fixture.eligibleID)

	start := templateRequest(fixture.handler, http.MethodPost, "/api/v1/servers/"+fixture.eligibleID+"/update", nil, &owner, true)
	if start.Code != http.StatusAccepted {
		t.Fatalf("start=%d %s", start.Code, start.Body.String())
	}
	var job map[string]any
	json.NewDecoder(start.Body).Decode(&job)
	jobID := job["id"].(string)

	if response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/server-update-jobs/"+jobID, nil, &other, false); response.Code != http.StatusNotFound {
		t.Fatalf("non-owner read=%d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/server-update-jobs/"+jobID+"/cancel", nil, &other, true); response.Code != http.StatusNotFound {
		t.Fatalf("non-owner cancel=%d %s", response.Code, response.Body.String())
	}
	cancel := templateRequest(fixture.handler, http.MethodPost, "/api/v1/server-update-jobs/"+jobID+"/cancel", nil, &owner, true)
	if cancel.Code != http.StatusOK {
		t.Fatalf("owner cancel=%d %s", cancel.Code, cancel.Body.String())
	}
	close(block)
	final := waitServerUpdateJob(t, fixture, owner, jobID)
	if final["status"] != "cancelled" {
		t.Fatalf("expected cancelled, got %#v", final)
	}
}
