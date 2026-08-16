package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/auth"
	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
)

type rbacSession struct {
	cookie *http.Cookie
	csrf   string
}

func TestRBACScopesAcrossAssignmentsListingsDashboardAndCapabilities(t *testing.T) {
	handler, db := newTestServer(t)
	ctx := context.Background()
	authentication := auth.New(db)
	admin, err := authentication.CreateInitialAdmin(ctx, "scope-admin", "scope-admin@example.test", "a password long enough")
	if err != nil {
		t.Fatal(err)
	}
	adminSession := newRBACSession(t, authentication, admin)
	identities := identity.New(db)
	globalUser := createRBACUser(t, identities, "global-viewer")
	directUser := createRBACUser(t, identities, "direct-operator")
	groupUser := createRBACUser(t, identities, "group-operator")
	startOnlyUser := createRBACUser(t, identities, "start-only")
	noAccessUser := createRBACUser(t, identities, "no-access")
	group, err := identities.CreateGroup(ctx, identity.CreateGroupInput{Name: "Minecraft Operators"})
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.AddMember(ctx, group.ID, groupUser.ID); err != nil {
		t.Fatal(err)
	}
	serverA := createRBACAPIServer(t, db, "RBAC server A")
	serverB := createRBACAPIServer(t, db, "RBAC server B")

	authorization := rbac.New(db)
	globalViewer := createRBACRole(t, authorization, "Global Server Viewer", []string{"Server.View"})
	operator := createRBACRole(t, authorization, "Server Operator", []string{"Server.View", "Server.Start", "Server.Stop", "Console.View"})
	startOnly := createRBACRole(t, authorization, "Start Only", []string{"Server.Start"})
	mixed := createRBACRole(t, authorization, "Mixed Administration", []string{"Server.View", "Users.View"})
	empty, err := authorization.CreateRole(ctx, "Empty Role API", "")
	if err != nil {
		t.Fatal(err)
	}

	globalSession := newRBACSession(t, authentication, auth.User{ID: globalUser.ID, Username: globalUser.Username})
	directSession := newRBACSession(t, authentication, auth.User{ID: directUser.ID, Username: directUser.Username})
	groupSession := newRBACSession(t, authentication, auth.User{ID: groupUser.ID, Username: groupUser.Username})
	startOnlySession := newRBACSession(t, authentication, auth.User{ID: startOnlyUser.ID, Username: startOnlyUser.Username})
	noAccessSession := newRBACSession(t, authentication, auth.User{ID: noAccessUser.ID, Username: noAccessUser.Username})

	postAssignment := func(subjectType, subjectID, roleID, scopeType string, scopeID *string) *httptest.ResponseRecorder {
		body := map[string]any{"role_id": roleID, "scope_type": scopeType}
		if scopeID != nil {
			body["scope_id"] = *scopeID
		}
		encoded, _ := json.Marshal(body)
		return rbacAPIRequest(handler, adminSession, http.MethodPost, "/api/v1/"+subjectType+"/"+subjectID+"/roles", encoded)
	}
	if response := postAssignment("users", globalUser.ID, globalViewer.ID, "global", nil); response.Code != http.StatusCreated {
		t.Fatalf("global assignment: %d %s", response.Code, response.Body.String())
	}
	if response := postAssignment("users", directUser.ID, operator.ID, "server", &serverA.Server.ID); response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), operator.ID) {
		t.Fatalf("direct server assignment: %d %s", response.Code, response.Body.String())
	}
	if response := postAssignment("groups", group.ID, operator.ID, "server", &serverA.Server.ID); response.Code != http.StatusCreated {
		t.Fatalf("group server assignment: %d %s", response.Code, response.Body.String())
	}
	if response := postAssignment("users", startOnlyUser.ID, startOnly.ID, "server", &serverA.Server.ID); response.Code != http.StatusCreated {
		t.Fatalf("start-only assignment: %d %s", response.Code, response.Body.String())
	}
	if response := postAssignment("users", directUser.ID, mixed.ID, "server", &serverA.Server.ID); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "cannot be assigned at server scope") {
		t.Fatalf("mixed server assignment: %d %s", response.Code, response.Body.String())
	}
	if response := postAssignment("users", directUser.ID, empty.ID, "server", &serverA.Server.ID); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "no permissions") {
		t.Fatalf("empty server assignment: %d %s", response.Code, response.Body.String())
	}
	if response := postAssignment("users", directUser.ID, operator.ID, "invalid", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope: %d %s", response.Code, response.Body.String())
	}
	if response := postAssignment("users", directUser.ID, operator.ID, "server", &serverA.Server.ID); response.Code != http.StatusConflict {
		t.Fatalf("duplicate assignment: %d %s", response.Code, response.Body.String())
	}

	assertServerList := func(session rbacSession, wantIDs ...string) {
		t.Helper()
		response := rbacAPIRequest(handler, session, http.MethodGet, "/api/v1/servers", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("server list: %d %s", response.Code, response.Body.String())
		}
		var payload struct {
			Servers []struct {
				Server struct {
					ID string `json:"id"`
				} `json:"server"`
				Capabilities []string `json:"capabilities"`
			} `json:"servers"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Servers) != len(wantIDs) {
			t.Fatalf("visible servers = %#v, want %#v", payload.Servers, wantIDs)
		}
		for index, wantID := range wantIDs {
			if payload.Servers[index].Server.ID != wantID {
				t.Fatalf("visible server %d = %s, want %s", index, payload.Servers[index].Server.ID, wantID)
			}
		}
	}
	assertServerList(globalSession, serverA.Server.ID, serverB.Server.ID)
	assertServerList(directSession, serverA.Server.ID)
	assertServerList(groupSession, serverA.Server.ID)
	assertServerList(startOnlySession)
	assertServerList(noAccessSession)

	for name, session := range map[string]rbacSession{"global": globalSession, "direct": directSession, "group": groupSession, "none": noAccessSession} {
		response := rbacAPIRequest(handler, session, http.MethodGet, "/api/v1/dashboard", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s dashboard: %d %s", name, response.Code, response.Body.String())
		}
		wantTotal := `"total":0`
		if name == "global" {
			wantTotal = `"total":2`
		} else if name == "direct" || name == "group" {
			wantTotal = `"total":1`
		}
		if !strings.Contains(response.Body.String(), wantTotal) {
			t.Fatalf("%s dashboard does not contain %s: %s", name, wantTotal, response.Body.String())
		}
	}

	detail := rbacAPIRequest(handler, directSession, http.MethodGet, "/api/v1/servers/"+serverA.Server.ID, nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"Server.View"`) || !strings.Contains(detail.Body.String(), `"Server.Start"`) {
		t.Fatalf("direct capabilities: %d %s", detail.Code, detail.Body.String())
	}
	other := rbacAPIRequest(handler, directSession, http.MethodGet, "/api/v1/servers/"+serverB.Server.ID, nil)
	if other.Code != http.StatusForbidden {
		t.Fatalf("other server detail: %d %s", other.Code, other.Body.String())
	}
	startOnlyDetail := rbacAPIRequest(handler, startOnlySession, http.MethodGet, "/api/v1/servers/"+serverA.Server.ID, nil)
	if startOnlyDetail.Code != http.StatusForbidden {
		t.Fatalf("Start without View exposed server detail: %d %s", startOnlyDetail.Code, startOnlyDetail.Body.String())
	}

	rolesResponse := rbacAPIRequest(handler, adminSession, http.MethodGet, "/api/v1/roles", nil)
	if rolesResponse.Code != http.StatusOK || !strings.Contains(rolesResponse.Body.String(), `"server_assignable":true`) || !strings.Contains(rolesResponse.Body.String(), `"server_assignable":false`) {
		t.Fatalf("role suitability response: %d %s", rolesResponse.Code, rolesResponse.Body.String())
	}
	accessResponse := rbacAPIRequest(handler, adminSession, http.MethodGet, "/api/v1/servers/"+serverA.Server.ID+"/access", nil)
	if accessResponse.Code != http.StatusOK || !strings.Contains(accessResponse.Body.String(), directUser.Username) || !strings.Contains(accessResponse.Body.String(), group.Name) {
		t.Fatalf("server access list: %d %s", accessResponse.Code, accessResponse.Body.String())
	}
	permissionsResponse := rbacAPIRequest(handler, adminSession, http.MethodGet, "/api/v1/permissions", nil)
	if permissionsResponse.Code != http.StatusOK ||
		!strings.Contains(permissionsResponse.Body.String(), `"allowed_scopes":["global","tenant","server"],"category":"Server","description":"View servers","key":"Server.View"`) ||
		!strings.Contains(permissionsResponse.Body.String(), `"allowed_scopes":["global"],"category":"Identity","description":"View users","key":"Users.View"`) ||
		!strings.Contains(permissionsResponse.Body.String(), `"allowed_scopes":["global","tenant"],"category":"Server","description":"Create servers","key":"Server.Create"`) ||
		!strings.Contains(permissionsResponse.Body.String(), `"allowed_scopes":["global"],"category":"Tenants","description":"View tenant entities","key":"Tenants.View"`) {
		t.Fatalf("permission scopes: %d %s", permissionsResponse.Code, permissionsResponse.Body.String())
	}

	assignments, err := authorization.ListUserAssignments(ctx, directUser.ID)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("direct assignments: %v, %v", assignments, err)
	}
	remove := rbacAPIRequest(handler, adminSession, http.MethodDelete, "/api/v1/users/"+directUser.ID+"/roles/"+assignments[0].ID, nil)
	if remove.Code != http.StatusNoContent {
		t.Fatalf("remove assignment: %d %s", remove.Code, remove.Body.String())
	}
	assertServerList(directSession)
}

func createRBACUser(t *testing.T, service *identity.Service, username string) identity.User {
	t.Helper()
	user, err := service.CreateUser(context.Background(), identity.CreateUserInput{Username: username, Email: username + "@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func createRBACRole(t *testing.T, service *rbac.Service, name string, permissions []string) rbac.Role {
	t.Helper()
	role, err := service.CreateRole(context.Background(), name, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(context.Background(), role.ID, permissions); err != nil {
		t.Fatal(err)
	}
	return role
}

func newRBACSession(t *testing.T, service *auth.Service, user auth.User) rbacSession {
	t.Helper()
	raw, csrf, err := service.CreateSession(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	return rbacSession{cookie: &http.Cookie{Name: "gamenode_session", Value: raw}, csrf: csrf}
}

func rbacAPIRequest(handler http.Handler, session rbacSession, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.AddCookie(session.cookie)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", session.csrf)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func createRBACAPIServer(t *testing.T, db *sql.DB, name string) servers.Record {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	service := servers.NewService(servers.NewStore(db), runtime.NewNative())
	record, err := service.Create(context.Background(), servers.Server{
		CreationMode:         "adopt",
		Name:                 name,
		WorkingDirectory:     filepath.Dir(executable),
		Executable:           executable,
		Arguments:            []string{},
		EnvironmentVariables: map[string]string{},
		RuntimeType:          "native",
		RestartPolicy:        "never",
		StopMethod:           "terminate",
		StopTimeoutSeconds:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}
