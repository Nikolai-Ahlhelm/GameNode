package api_test

import (
	"bytes"
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
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
)

type apiInstaller struct {
	block   chan struct{}
	started chan struct{}
	fail    bool
}

type apiCatalogSource struct{ catalog, template []byte }

func (s apiCatalogSource) FetchCatalog(context.Context) ([]byte, error) { return s.catalog, nil }
func (s apiCatalogSource) FetchTemplate(context.Context, string) ([]byte, error) {
	return s.template, nil
}

func (i *apiInstaller) Install(ctx context.Context, root string, _ steamcmd.InstallPlan, output io.Writer, sink steamcmd.EventSink) error {
	if output != nil {
		_, _ = io.WriteString(output, "Steam> Installing with password TOP_SECRET\n")
	}
	if sink != nil {
		sink(steamcmd.Event{Phase: "installing", Summary: "Installing game files"})
	}
	if i.started != nil {
		select {
		case i.started <- struct{}{}:
		default:
		}
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
	return os.WriteFile(filepath.Join(root, "game.exe"), []byte("test"), 0600)
}

type provisionAPIFixture struct {
	handler   http.Handler
	db        *sql.DB
	template  templates.Template
	service   *provisioning.Service
	installer *apiInstaller
}

func newProvisionAPI(t *testing.T, installer *apiInstaller) provisionAPIFixture {
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
	templateService := templates.NewService(templates.NewStore(db))
	min, max := float64(1), float64(65535)
	template, err := templates.NewStore(db).Create(context.Background(), templates.Template{Name: "Windows Fixture", Description: "provisioning", SourceType: templates.SourcePelicanPterodactyl, Installer: templates.InstallerDefinition{Type: templates.InstallerSteamCMD, SteamCMD: &templates.SteamCMDPlan{AppID: 10, Validate: true, LoginMode: "anonymous", Platform: "native", InstallTarget: "server_root"}}, Launch: &templates.LaunchDefinition{Executable: "game.exe", Arguments: []string{"-port=${PORT}", "-mode=dedicated"}, Environment: map[string]string{"PASSWORD": "${PASSWORD}"}, WorkingRoot: "server_root"}, Variables: []templates.TemplateVariable{{Name: "Port", Key: "PORT", DefaultValue: "27000", UserEditable: true, Type: "integer", Required: true, Validation: templates.Validation{Min: &min, Max: &max}}, {Name: "Password", Key: "PASSWORD", DefaultValue: "", UserEditable: true, Type: "secret", Sensitive: true, Nullable: true}}, Compatibility: templates.Compatibility{Status: templates.PartiallyCompatible}})
	if err != nil {
		t.Fatal(err)
	}
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	service := provisioning.NewWithOptions(db, templateService, installer, serverService, t.TempDir(), provisioning.Options{HostOS: "windows"})
	t.Cleanup(service.Close)
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{Templates: templateService, Provisioning: service}).Handler(http.NotFoundHandler())
	return provisionAPIFixture{handler, db, template, service, installer}
}

func provisionBody(template templates.Template, name, directory, secret string) []byte {
	body, _ := json.Marshal(map[string]any{"server_name": name, "directory_name": directory, "variables": map[string]string{"PORT": "27001", "PASSWORD": secret}})
	return body
}
func waitAPIJob(t *testing.T, fixture provisionAPIFixture, session testSession, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := templateRequest(fixture.handler, http.MethodGet, "/api/v1/provisioning/jobs/"+id, nil, &session, false)
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
	t.Fatal("job did not complete")
	return nil
}

func TestProvisioningAPIAuthCSRFAuditAndSecretRedaction(t *testing.T) {
	fixture := newProvisionAPI(t, &apiInstaller{})
	path := "/api/v1/templates/" + fixture.template.ID + "/provision"
	if response := templateRequest(fixture.handler, http.MethodPost, path, provisionBody(fixture.template, "Seven", "seven", "TOP_SECRET"), nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauth=%d", response.Code)
	}
	admin := createAdminSession(t, fixture.handler)
	if response := templateRequest(fixture.handler, http.MethodPost, path, provisionBody(fixture.template, "Seven", "seven", "TOP_SECRET"), &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("csrf=%d", response.Code)
	}
	response := templateRequest(fixture.handler, http.MethodPost, path, provisionBody(fixture.template, "Seven", "seven", "TOP_SECRET"), &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start=%d %s", response.Code, response.Body.String())
	}
	var created map[string]any
	json.NewDecoder(response.Body).Decode(&created)
	job := waitAPIJob(t, fixture, admin, created["id"].(string))
	if job["status"] != "completed" || strings.Contains(mustJSON(job), "TOP_SECRET") || !strings.Contains(mustJSON(job), "[REDACTED]") {
		t.Fatalf("job=%#v", job)
	}
	serverID := job["server_id"].(string)
	serverResponse := templateRequest(fixture.handler, http.MethodGet, "/api/v1/servers/"+serverID, nil, &admin, false)
	if serverResponse.Code != http.StatusOK || strings.Contains(serverResponse.Body.String(), "TOP_SECRET") || !strings.Contains(serverResponse.Body.String(), `"sensitive_environment_variables":["PASSWORD"]`) {
		t.Fatalf("server response=%d %s", serverResponse.Code, serverResponse.Body.String())
	}
	var actions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('server.provision_start','server.provision_complete')`).Scan(&actions); err != nil || actions != 2 {
		t.Fatalf("audit actions=%d err=%v", actions, err)
	}
	rows, err := fixture.db.Query(`SELECT COALESCE(metadata_json,'')||COALESCE(error_summary,'') FROM audit_log WHERE action LIKE 'server.provision_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var text string
		rows.Scan(&text)
		if strings.Contains(text, "TOP_SECRET") || strings.Contains(text, "game.exe") || strings.Contains(text, fixture.template.Launch.Arguments[1]) {
			t.Fatalf("unsafe audit=%q", text)
		}
	}
}

func TestProvisioningRequiresTemplatesViewAndServerCreate(t *testing.T) {
	fixture := newProvisionAPI(t, &apiInstaller{})
	createAdminSession(t, fixture.handler)
	ctx := context.Background()
	identities := identity.New(fixture.db)
	authorization := rbac.New(fixture.db)
	makeUser := func(name string, permissions []string) testSession {
		user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: name, Email: name + "@example.test", Password: "a password long enough"})
		if err != nil {
			t.Fatal(err)
		}
		role, err := authorization.CreateRole(ctx, name+"-role", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = authorization.ReplacePermissions(ctx, role.ID, permissions); err != nil {
			t.Fatal(err)
		}
		if err = authorization.AssignUser(ctx, user.ID, role.ID, rbac.Scope{Type: "global"}); err != nil {
			t.Fatal(err)
		}
		return loginSession(t, fixture.handler, name)
	}
	path := "/api/v1/templates/" + fixture.template.ID + "/provision"
	for _, test := range []struct {
		name        string
		permissions []string
	}{{"viewer", []string{"Templates.View"}}, {"creator", []string{"Server.Create"}}, {"manager", []string{"Templates.Manage", "Server.Create"}}} {
		t.Run(test.name, func(t *testing.T) {
			session := makeUser(test.name, test.permissions)
			response := templateRequest(fixture.handler, http.MethodPost, path, provisionBody(fixture.template, test.name, test.name, ""), &session, true)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d %s", response.Code, response.Body.String())
			}
		})
	}
	allowed := makeUser("allowed", []string{"Templates.View", "Server.Create"})
	response := templateRequest(fixture.handler, http.MethodPost, path, provisionBody(fixture.template, "Allowed", "allowed", ""), &allowed, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("allowed=%d %s", response.Code, response.Body.String())
	}
}

func TestProvisioningCancelAndSanitizedFailure(t *testing.T) {
	installer := &apiInstaller{block: make(chan struct{}), started: make(chan struct{}, 1)}
	fixture := newProvisionAPI(t, installer)
	admin := createAdminSession(t, fixture.handler)
	path := "/api/v1/templates/" + fixture.template.ID + "/provision"
	response := templateRequest(fixture.handler, http.MethodPost, path, provisionBody(fixture.template, "Seven", "seven", "CANCEL_SECRET"), &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start=%d", response.Code)
	}
	var job map[string]any
	json.NewDecoder(response.Body).Decode(&job)
	select {
	case <-installer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("installer not started")
	}
	cancel := templateRequest(fixture.handler, http.MethodPost, "/api/v1/provisioning/jobs/"+job["id"].(string)+"/cancel", nil, &admin, true)
	if cancel.Code != http.StatusOK || strings.Contains(cancel.Body.String(), "CANCEL_SECRET") {
		t.Fatalf("cancel=%d %s", cancel.Code, cancel.Body.String())
	}
	close(installer.block)
	terminal := waitAPIJob(t, fixture, admin, job["id"].(string))
	if terminal["status"] != "cancelled" {
		t.Fatalf("job=%#v", terminal)
	}
	var serversCount int
	fixture.db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&serversCount)
	if serversCount != 0 {
		t.Fatal("cancel created server")
	}
	var cancels int
	fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='server.provision_cancel'`).Scan(&cancels)
	if cancels != 1 {
		t.Fatalf("cancel audit=%d", cancels)
	}
	installer.block = nil
	installer.fail = true
	failedResponse := templateRequest(fixture.handler, http.MethodPost, path, provisionBody(fixture.template, "Failed", "failed", "FAIL_SECRET"), &admin, true)
	if failedResponse.Code != http.StatusAccepted {
		t.Fatalf("failure start=%d %s", failedResponse.Code, failedResponse.Body.String())
	}
	json.NewDecoder(failedResponse.Body).Decode(&job)
	failed := waitAPIJob(t, fixture, admin, job["id"].(string))
	encoded, _ := json.Marshal(failed)
	if failed["status"] != "failed" || strings.Contains(string(encoded), "FAIL_SECRET") || strings.Contains(string(encoded), "deadline") {
		t.Fatalf("unsafe failure response: %s", encoded)
	}
	var failures int
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='server.provision_fail' AND result='failure' AND metadata_json NOT LIKE '%FAIL_SECRET%'`).Scan(&failures)
		if failures == 1 {
			break
		}
	}
	if failures != 1 {
		t.Fatalf("failure audit=%d", failures)
	}
}

func TestRetryRegistrationRequiresCSRFIsIdempotentAndAudited(t *testing.T) {
	fixture := newProvisionAPI(t, &apiInstaller{})
	admin := createAdminSession(t, fixture.handler)
	startPath := "/api/v1/templates/" + fixture.template.ID + "/provision"
	response := templateRequest(fixture.handler, http.MethodPost, startPath, provisionBody(fixture.template, "Retry API", "retry-api", "RETRY_SECRET"), &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start=%d %s", response.Code, response.Body.String())
	}
	var started map[string]any
	if err := json.NewDecoder(response.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	jobID, _ := started["id"].(string)
	completed := waitAPIJob(t, fixture, admin, jobID)
	serverID, _ := completed["server_id"].(string)
	if serverID == "" {
		t.Fatalf("completed job=%#v", completed)
	}
	if _, err := fixture.db.Exec(`UPDATE provisioning_jobs SET status='failed',current_phase='failed',server_id=NULL,registration_recoverable=1,failure_phase='registering_server',failure_code='INTERRUPTED' WHERE id=?`, jobID); err != nil {
		t.Fatal(err)
	}
	retryPath := "/api/v1/provisioning/jobs/" + jobID + "/retry-registration"
	if response = templateRequest(fixture.handler, http.MethodPost, retryPath, []byte(`{}`), &admin, false); response.Code != http.StatusForbidden {
		t.Fatalf("retry without CSRF=%d %s", response.Code, response.Body.String())
	}
	response = templateRequest(fixture.handler, http.MethodPost, retryPath, []byte(`{}`), &admin, true)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"server_id":"`+serverID+`"`) {
		t.Fatalf("retry=%d %s", response.Code, response.Body.String())
	}
	if response = templateRequest(fixture.handler, http.MethodPost, retryPath, []byte(`{}`), &admin, true); response.Code != http.StatusConflict {
		t.Fatalf("duplicate retry=%d %s", response.Code, response.Body.String())
	}
	var serversCount, retrySuccess, retryFailure int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&serversCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='server.provision_retry' AND result='success'`).Scan(&retrySuccess); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='server.provision_retry' AND result='failure'`).Scan(&retryFailure); err != nil {
		t.Fatal(err)
	}
	if serversCount != 1 || retrySuccess != 1 || retryFailure != 1 {
		t.Fatalf("servers=%d retry success=%d failure=%d", serversCount, retrySuccess, retryFailure)
	}
	var auditText string
	if err := fixture.db.QueryRow(`SELECT GROUP_CONCAT(COALESCE(metadata_json,'')||COALESCE(error_summary,''),'') FROM audit_log WHERE action='server.provision_retry'`).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditText, "RETRY_SECRET") {
		t.Fatalf("retry secret leaked to audit: %q", auditText)
	}
}

func TestProvisioningAPIRejectsUnsafeInputsWithoutLeaks(t *testing.T) {
	fixture := newProvisionAPI(t, &apiInstaller{})
	admin := createAdminSession(t, fixture.handler)
	path := "/api/v1/templates/" + fixture.template.ID + "/provision"
	for _, body := range [][]byte{[]byte(`{"server_name":"x","directory_name":"../escape","variables":{}}`), []byte(`{"server_name":"x","directory_name":"x","variables":{"UNKNOWN":"SECRET_VALUE"}}`), bytes.Repeat([]byte("x"), 129<<10)} {
		response := templateRequest(fixture.handler, http.MethodPost, path, body, &admin, true)
		if response.Code == http.StatusAccepted || strings.Contains(response.Body.String(), "SECRET_VALUE") {
			t.Fatalf("unsafe input=%d %s", response.Code, response.Body.String())
		}
	}
}

func TestOfficialSteamTemplateProvisioningAPI(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	minimum, maximum := float64(1024), float64(65535)
	template := templates.Template{SchemaVersion: 1, ID: "official-steam", Name: "Official Steam", Description: "Official fixture", Version: "1.0.0", Category: "steamcmd", SourceType: templates.SourceOfficial, SourceMetadata: templates.SourceMetadata{Author: "GameNode"}, ReadOnly: true, Platforms: []string{"windows"}, Installer: templates.InstallerDefinition{Type: templates.InstallerSteamCMD, SteamCMD: &templates.SteamCMDPlan{AppID: 294420, Validate: true, LoginMode: "anonymous", Platform: "native", InstallTarget: "server_root"}}, PlatformLaunches: map[string]templates.LaunchDefinition{"windows": {Executable: "game.exe", Arguments: []string{"-port={{PORT}}"}, WorkingRoot: "server_root", StopMethod: "terminate", StopTimeout: 30}}, Variables: []templates.TemplateVariable{{Name: "Port", Key: "PORT", DefaultValue: "26900", UserEditable: true, Type: "integer", Required: true, Validation: templates.Validation{Min: &minimum, Max: &maximum}}}, Compatibility: templates.Compatibility{Status: templates.Compatible}}
	entry := templates.CatalogEntry{ID: template.ID, Name: template.Name, Description: template.Description, Category: template.Category, Version: template.Version, TemplateSchemaVersion: 1, Platforms: template.Platforms, Installer: templates.InstallerSteamCMD, File: "steamcmd/official.json"}
	catalogData, _ := json.Marshal(templates.CatalogManifest{SchemaVersion: 1, Templates: []templates.CatalogEntry{entry}})
	templateData, _ := json.Marshal(template)
	catalog := templates.NewCatalogManager(apiCatalogSource{catalog: catalogData, template: templateData}, t.TempDir(), "0.2.0")
	if _, err = catalog.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	templateService := templates.NewServiceWithCatalog(templates.NewStore(db), catalog)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	provisioningService := provisioning.NewWithOptions(db, templateService, &apiInstaller{}, serverService, t.TempDir(), provisioning.Options{HostOS: "windows"})
	defer provisioningService.Close()
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{Templates: templateService, Provisioning: provisioningService}).Handler(http.NotFoundHandler())
	admin := createAdminSession(t, handler)
	path := "/api/v1/templates/" + template.ID + "/provision"
	body, _ := json.Marshal(map[string]any{"server_name": "Official", "directory_name": "official", "variables": map[string]string{"PORT": "26901"}})
	response := templateRequest(handler, http.MethodPost, path, body, &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start=%d %s", response.Code, response.Body.String())
	}
	var created map[string]any
	_ = json.NewDecoder(response.Body).Decode(&created)
	fixture := provisionAPIFixture{handler: handler, db: db, service: provisioningService, template: template}
	job := waitAPIJob(t, fixture, admin, created["id"].(string))
	if job["status"] != "completed" || job["server_id"] == "" {
		t.Fatalf("job=%#v", job)
	}
	var source, version string
	if err = db.QueryRow(`SELECT template_source,template_version FROM server_template_variables WHERE server_id=? LIMIT 1`, job["server_id"]).Scan(&source, &version); err != nil || source != templates.SourceOfficial || version != "1.0.0" {
		t.Fatalf("provenance=%s/%s err=%v", source, version, err)
	}
	unsafeBody := []byte(`{"server_name":"Injected","directory_name":"injected","variables":{"APP_ID":"10","PORT":"26902"}}`)
	if rejected := templateRequest(handler, http.MethodPost, path, unsafeBody, &admin, true); rejected.Code == http.StatusAccepted {
		t.Fatal("user-supplied App ID accepted")
	}
}

func mustJSON(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }
