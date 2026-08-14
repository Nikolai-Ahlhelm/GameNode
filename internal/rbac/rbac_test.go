package rbac

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"gamenode"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
)

func testDB(t *testing.T) *sql.DB {
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
	return db
}

func TestCatalogAndEvaluator(t *testing.T) {
	if !Known("Server.Start") || Known("made.up") {
		t.Fatal("catalog")
	}
	db, e := database.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if e = database.Migrate(db, gamenode.MigrationFiles); e != nil {
		t.Fatal(e)
	}
	a := auth.New(db)
	ctx := context.Background()
	if _, e = a.CreateInitialAdmin(ctx, "admin", "admin@example.test", "a password long enough"); e != nil {
		t.Fatal(e)
	}
	i := identity.New(db)
	u, e := i.CreateUser(ctx, identity.CreateUserInput{Username: "user", Email: "user@example.test", Password: "a password long enough"})
	if e != nil {
		t.Fatal(e)
	}
	s := New(db)
	r, e := s.CreateRole(ctx, "Operator", "")
	if e != nil {
		t.Fatal(e)
	}
	if named, createErr := s.CreateRole(ctx, "Minecraft Operator", "display role"); createErr != nil {
		t.Fatalf("create role with display name: %v", createErr)
	} else if named.Name != "Minecraft Operator" {
		t.Fatalf("role name = %q", named.Name)
	}
	if _, createErr := s.CreateRole(ctx, "invalid/role", ""); createErr == nil || !strings.Contains(createErr.Error(), "role name") {
		t.Fatalf("invalid role error = %v", createErr)
	}
	if e = s.ReplacePermissions(ctx, r.ID, []string{"Server.Start"}); e != nil {
		t.Fatal(e)
	}
	if e = s.AssignUser(ctx, u.ID, r.ID, Scope{Type: "global"}); e != nil {
		t.Fatal(e)
	}
	if e = s.AssignUser(ctx, u.ID, r.ID, Scope{Type: "global"}); !errors.Is(e, ErrDuplicateAssignment) {
		t.Fatalf("duplicate global assignment error = %v", e)
	}
	ok, e := s.Allowed(ctx, u.ID, "Server.Start", Scope{Type: "server", ID: ptr("x")})
	if e != nil || !ok {
		t.Fatalf("global allow %v %v", ok, e)
	}
	ok, e = s.Allowed(ctx, u.ID, "Server.Kill", Scope{Type: "global"})
	if e != nil || ok {
		t.Fatal("missing permission allowed")
	}
	disabled := false
	if _, e = i.UpdateUser(ctx, "", u.ID, identity.UpdateUserInput{Enabled: &disabled}); e != nil {
		t.Fatal(e)
	}
	ok, e = s.Allowed(ctx, u.ID, "Server.Start", Scope{Type: "global"})
	if e != nil || ok {
		t.Fatal("disabled allowed")
	}
}

func TestPermissionScopeMatrix(t *testing.T) {
	globalOnly := map[string]bool{
		"Server.Create": true, "Users.View": true, "Users.Manage": true,
		"Groups.View": true, "Groups.Manage": true, "Roles.View": true, "Roles.Manage": true,
		"Settings.View": true, "Settings.Manage": true, "Log.Read": true, "Log.FlushDirectory": true,
		"Templates.View": true, "Templates.Manage": true, "Audit.View": true,
	}
	if len(Catalog) != 32 {
		t.Fatalf("catalog contains %d permissions; update the explicit scope matrix test", len(Catalog))
	}
	for _, permission := range Catalog {
		want := []string{"global", "server"}
		if globalOnly[permission.Key] {
			want = []string{"global"}
		}
		got := AllowedScopes(permission.Key)
		if len(got) != len(want) || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("AllowedScopes(%s) = %v, want %v", permission.Key, got, want)
		}
	}
}

func TestGroupAssignmentsAreImmediateAndScoped(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "member", Email: "member@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := identities.CreateGroup(ctx, identity.CreateGroupInput{Name: "operators"})
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.AddMember(ctx, group.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	service := New(db)
	role, err := service.CreateRole(ctx, "operators-role", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Files.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignGroup(ctx, group.ID, role.ID, Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
	allowed, err := service.Allowed(ctx, user.ID, "Files.View", Scope{Type: "server", ID: ptr("server-a")})
	if err != nil || !allowed {
		t.Fatalf("group global permission = %v, %v", allowed, err)
	}
	if err = identities.RemoveMember(ctx, group.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	allowed, err = service.Allowed(ctx, user.ID, "Files.View", Scope{Type: "server", ID: ptr("server-a")})
	if err != nil || allowed {
		t.Fatalf("removed membership still allowed: %v, %v", allowed, err)
	}
}

func TestPlatformPermissionsAreGlobalOnly(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "manager", Email: "manager@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := identities.CreateGroup(ctx, identity.CreateGroupInput{Name: "managers"})
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.AddMember(ctx, group.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	service := New(db)
	role, err := service.CreateRole(ctx, "platform-manager", "")
	if err != nil {
		t.Fatal(err)
	}
	platformPermissions := []string{"Users.View", "Users.Manage", "Groups.View", "Groups.Manage", "Roles.View", "Roles.Manage", "Templates.View", "Templates.Manage"}
	if err = service.ReplacePermissions(ctx, role.ID, platformPermissions); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO servers(id,name,description,creation_mode,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", "server-a", "server", "", "custom", "C:/", "test.exe", "[]", "{}", "native", 0, "never", "terminate", "", 15, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "server", ID: ptr("server-a")}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("server-scoped user assignment error = %v", err)
	}
	if err = service.AssignGroup(ctx, group.ID, role.ID, Scope{Type: "server", ID: ptr("server-a")}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("server-scoped group assignment error = %v", err)
	}
	for _, permission := range platformPermissions {
		for _, scope := range []Scope{{Type: "global"}, {Type: "server", ID: ptr("server-a")}} {
			allowed, err := service.Allowed(ctx, user.ID, permission, scope)
			if err != nil || allowed {
				t.Fatalf("server-scoped %s at %#v = %v, %v", permission, scope, allowed, err)
			}
		}
	}
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
	for _, permission := range platformPermissions {
		allowed, err := service.Allowed(ctx, user.ID, permission, Scope{Type: "global"})
		if err != nil || !allowed {
			t.Fatalf("global direct %s = %v, %v", permission, allowed, err)
		}
	}
	if err = service.AssignGroup(ctx, group.ID, role.ID, Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
	if err = service.RemoveUserAssignmentFor(ctx, user.ID, mustFirstAssignment(t, service, ctx, user.ID)); err != nil {
		t.Fatal(err)
	}
	for _, permission := range platformPermissions {
		allowed, err := service.Allowed(ctx, user.ID, permission, Scope{Type: "global"})
		if err != nil || !allowed {
			t.Fatalf("global group %s = %v, %v", permission, allowed, err)
		}
	}
}

func TestServerAssignmentRejectsGlobalOnlyPermissions(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	users := identity.New(db)
	u, err := users.CreateUser(ctx, identity.CreateUserInput{Username: "scoped", Email: "scoped@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	service := New(db)
	role, err := service.CreateRole(ctx, "platform-only", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Settings.Manage"}); err != nil {
		t.Fatal(err)
	}
	serverID := "server-for-scope-test"
	if _, err = db.ExecContext(ctx, "INSERT INTO servers(id,name,description,creation_mode,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", serverID, "scope", "", "custom", "C:/", "x", "[]", "{}", "native", 0, "never", "terminate", "", 15, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, u.ID, role.ID, Scope{Type: "server", ID: &serverID}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("expected invalid scope, got %v", err)
	}
}

func TestRoleServerAssignableSemantics(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	service := New(db)
	tests := []struct {
		name        string
		permissions []string
		want        bool
	}{
		{name: "empty", permissions: nil, want: false},
		{name: "server permissions", permissions: []string{"Server.View", "Server.Start", "Console.View"}, want: true},
		{name: "global only", permissions: []string{"Users.View"}, want: false},
		{name: "mixed", permissions: []string{"Users.View", "Server.View"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, err := service.CreateRole(ctx, "role-"+strings.ReplaceAll(tt.name, " ", "-"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err = service.ReplacePermissions(ctx, role.ID, tt.permissions); err != nil {
				t.Fatal(err)
			}
			role, err = service.GetRole(ctx, role.ID)
			if err != nil {
				t.Fatal(err)
			}
			if role.ServerAssignable != tt.want {
				t.Fatalf("server_assignable = %t, want %t (%v)", role.ServerAssignable, tt.want, role.Permissions)
			}
		})
	}
}

func TestServerAssignmentsDirectGroupGlobalAndIndependent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	direct, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "direct", Email: "direct@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "member-two", Email: "member-two@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	none, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "none", Email: "none@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "admin-two", Email: "admin-two@example.test", Password: "a password long enough", IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	group, err := identities.CreateGroup(ctx, identity.CreateGroupInput{Name: "server operators"})
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.AddMember(ctx, group.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	insertRBACServer(t, db, "server-a")
	insertRBACServer(t, db, "server-b")

	service := New(db)
	operator, err := service.CreateRole(ctx, "Server Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, operator.ID, []string{"Server.View", "Server.Start", "Server.Stop", "Console.View"}); err != nil {
		t.Fatal(err)
	}
	serverA := "server-a"
	if err = service.AssignUser(ctx, direct.ID, operator.ID, Scope{Type: "server", ID: &serverA}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignGroup(ctx, group.ID, operator.ID, Scope{Type: "server", ID: &serverA}); err != nil {
		t.Fatal(err)
	}

	viewer, err := service.CreateRole(ctx, "Global Server Viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, viewer.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, none.ID, viewer.ID, Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}

	assertAllowed := func(userID, permission, server string, want bool) {
		t.Helper()
		allowed, evalErr := service.Allowed(ctx, userID, permission, Scope{Type: "server", ID: &server})
		if evalErr != nil || allowed != want {
			t.Fatalf("Allowed(%s, %s, %s) = %t, %v; want %t", userID, permission, server, allowed, evalErr, want)
		}
	}
	for _, userID := range []string{direct.ID, member.ID} {
		assertAllowed(userID, "Server.View", "server-a", true)
		assertAllowed(userID, "Server.Start", "server-a", true)
		assertAllowed(userID, "Server.View", "server-b", false)
	}
	assertAllowed(none.ID, "Server.View", "server-a", true)
	assertAllowed(none.ID, "Server.View", "server-b", true)
	assertAllowed(none.ID, "Server.Start", "server-a", false)
	assertAllowed(admin.ID, "Server.View", "server-b", true)
	assertAllowed(admin.ID, "Server.Start", "server-b", true)
}

func TestRolePermissionUpdateCannotInvalidateServerAssignment(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "assigned", Email: "assigned@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	insertRBACServer(t, db, "assigned-server")
	service := New(db)
	role, err := service.CreateRole(ctx, "Assigned Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	serverID := "assigned-server"
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "server", ID: &serverID}); err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View", "Users.View"}); !errors.Is(err, ErrRoleHasServerAssignments) {
		t.Fatalf("mixed permission update error = %v", err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, nil); !errors.Is(err, ErrRoleHasServerAssignments) {
		t.Fatalf("empty permission update error = %v", err)
	}
	permissions, err := service.GetRolePermissions(ctx, role.ID)
	if err != nil || len(permissions) != 1 || permissions[0] != "Server.View" {
		t.Fatalf("permissions changed after rejected update: %v, %v", permissions, err)
	}
}

func TestEmptyRoleCannotBeAssignedToServer(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "empty-role-user", Email: "empty-role@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	insertRBACServer(t, db, "empty-role-server")
	service := New(db)
	role, err := service.CreateRole(ctx, "Empty Role", "")
	if err != nil {
		t.Fatal(err)
	}
	serverID := "empty-role-server"
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "server", ID: &serverID}); !errors.Is(err, ErrEmptyServerRole) {
		t.Fatalf("empty server role assignment error = %v", err)
	}
}

func insertRBACServer(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO servers(id,name,description,creation_mode,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", id, id, "", "custom", "C:/", "test.exe", "[]", "{}", "native", 0, "never", "terminate", "", 15, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}
func mustFirstAssignment(t *testing.T, service *Service, ctx context.Context, user string) string {
	t.Helper()
	assignments, err := service.ListUserAssignments(ctx, user)
	if err != nil || len(assignments) < 1 {
		t.Fatalf("list user assignments: %v", err)
	}
	for _, assignment := range assignments {
		if assignment.Scope.Type == "global" {
			return assignment.ID
		}
	}
	t.Fatal("missing global assignment")
	return ""
}
func ptr(s string) *string { return &s }
