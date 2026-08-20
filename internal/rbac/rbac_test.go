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
	"gamenode/internal/tenants"
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
		"Users.View": true, "Users.Manage": true,
		"Groups.View": true, "Groups.Manage": true, "Roles.View": true, "Roles.Manage": true,
		"Settings.View": true, "Settings.Manage": true, "Log.Read": true, "Log.FlushDirectory": true,
		"Templates.View": true, "Templates.Manage": true, "Audit.View": true,
		"Tenants.View": true, "Tenants.Manage": true,
		"Node.View": true, "Node.Manage": true,
	}
	// Server.Create is the one deliberate exception: it supports "global"
	// and "tenant" but never "server" (a server does not exist yet at the
	// moment it is evaluated).
	// Cluster.View/Cluster.Schedule (v0.6) share the same "global and tenant
	// only" rule as Server.Create: a placement decision is evaluated before
	// any server exists, so a per-server scope is meaningless.
	globalAndTenantOnly := map[string]bool{
		"Server.Create": true, "Cluster.View": true, "Cluster.Schedule": true,
		// RemoteServer/RemoteConsole/RemoteFiles/RemoteMonitoring (v0.5B/v0.5C)
		// have no local per-remote-server assignment row to scope against -
		// see internal/rbac/catalog.go's isRemoteServerPermission.
		"RemoteServer.View": true, "RemoteServer.Manage": true,
		"RemoteConsole.View": true, "RemoteConsole.Send": true,
		"RemoteFiles.View": true, "RemoteFiles.Edit": true, "RemoteFiles.Upload": true, "RemoteFiles.Download": true, "RemoteFiles.Delete": true, "RemoteFiles.Rename": true,
		"RemoteMonitoring.View": true,
	}
	if len(Catalog) != 54 {
		t.Fatalf("catalog contains %d permissions; update the explicit scope matrix test", len(Catalog))
	}
	for _, permission := range Catalog {
		want := []string{"global", "tenant", "server"}
		switch {
		case globalOnly[permission.Key]:
			want = []string{"global"}
		case globalAndTenantOnly[permission.Key]:
			want = []string{"global", "tenant"}
		}
		got := AllowedScopes(permission.Key)
		if len(got) != len(want) || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("AllowedScopes(%s) = %v, want %v", permission.Key, got, want)
		}
		for _, scopeType := range []string{"global", "tenant", "server"} {
			want := len(want) > 0 && contains(want, scopeType)
			if got := ScopeAllowed(permission.Key, scopeType); got != want {
				t.Errorf("ScopeAllowed(%s, %s) = %t, want %t", permission.Key, scopeType, got, want)
			}
		}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	if _, err = db.ExecContext(ctx, "INSERT INTO servers(id,tenant_id,name,description,creation_mode,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", "server-a", "default", "server", "", "custom", "C:/", "test.exe", "[]", "{}", "native", 0, "never", "terminate", "", 15, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
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
	if _, err = db.ExecContext(ctx, "INSERT INTO servers(id,tenant_id,name,description,creation_mode,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", serverID, "default", "scope", "", "custom", "C:/", "x", "[]", "{}", "native", 0, "never", "terminate", "", 15, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
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

// TestTenantScopeDirectAndGroupAssignment covers the direct and group tenant
// assignment scenarios from GameNode_Tenant_Foundation_Prompt.md section 3.7:
// Alice, directly assigned Server Viewer at Tenant A, sees both of Tenant
// A's servers and none of Tenant B's; Bob gets the identical effective
// access purely through group membership at Tenant A.
func TestTenantScopeDirectAndGroupAssignment(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	alice, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "alice", Email: "alice@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "bob", Email: "bob@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	operators, err := identities.CreateGroup(ctx, identity.CreateGroupInput{Name: "Operators"})
	if err != nil {
		t.Fatal(err)
	}
	if err = identities.AddMember(ctx, operators.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	tenantB := createRBACTenant(t, db, "Tenant B")
	insertRBACServerForTenant(t, db, "a1", tenantA.ID)
	insertRBACServerForTenant(t, db, "a2", tenantA.ID)
	insertRBACServerForTenant(t, db, "b1", tenantB.ID)

	service := New(db)
	viewer, err := service.CreateRole(ctx, "Server Viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, viewer.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, alice.ID, viewer.ID, Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignGroup(ctx, operators.ID, viewer.ID, Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}

	for _, userID := range []string{alice.ID, bob.ID} {
		for _, server := range []string{"a1", "a2"} {
			allowed, err := service.Allowed(ctx, userID, "Server.View", Scope{Type: "server", ID: ptr(server)})
			if err != nil || !allowed {
				t.Fatalf("Allowed(%s, Server.View, %s) = %t, %v; want true", userID, server, allowed, err)
			}
		}
		allowed, err := service.Allowed(ctx, userID, "Server.View", Scope{Type: "server", ID: ptr("b1")})
		if err != nil || allowed {
			t.Fatalf("Allowed(%s, Server.View, b1) = %t, %v; want false (different tenant)", userID, allowed, err)
		}
	}
	// A tenant grant is also directly visible when the caller evaluates
	// tenant scope itself (e.g. a future "may I create a server in this
	// tenant" check), not only when resolved through a server.
	if allowed, err := service.Allowed(ctx, alice.ID, "Server.View", Scope{Type: "tenant", ID: &tenantA.ID}); err != nil || !allowed {
		t.Fatalf("Allowed(alice, Server.View, tenant A) = %t, %v; want true", allowed, err)
	}
	if allowed, err := service.Allowed(ctx, alice.ID, "Server.View", Scope{Type: "tenant", ID: &tenantB.ID}); err != nil || allowed {
		t.Fatalf("Allowed(alice, Server.View, tenant B) = %t, %v; want false", allowed, err)
	}
}

// TestGlobalAssignmentSeesAllTenants proves a global grant applies across
// every tenant's servers, not just one.
func TestGlobalAssignmentSeesAllTenants(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "global-viewer", Email: "global-viewer@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	tenantB := createRBACTenant(t, db, "Tenant B")
	insertRBACServerForTenant(t, db, "ga1", tenantA.ID)
	insertRBACServerForTenant(t, db, "gb1", tenantB.ID)
	service := New(db)
	role, err := service.CreateRole(ctx, "Global Viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
	for _, server := range []string{"ga1", "gb1"} {
		allowed, err := service.Allowed(ctx, user.ID, "Server.View", Scope{Type: "server", ID: ptr(server)})
		if err != nil || !allowed {
			t.Fatalf("Allowed(global user, Server.View, %s) = %t, %v; want true", server, allowed, err)
		}
	}
}

// TestServerScopedAssignmentDoesNotLeakToTenantSiblings proves a
// server-specific grant applies to exactly that one server, even when a
// sibling server shares the same tenant.
func TestServerScopedAssignmentDoesNotLeakToTenantSiblings(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "server-scoped", Email: "server-scoped@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	insertRBACServerForTenant(t, db, "sa1", tenantA.ID)
	insertRBACServerForTenant(t, db, "sa2", tenantA.ID)
	service := New(db)
	role, err := service.CreateRole(ctx, "One Server", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	serverA1 := "sa1"
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "server", ID: &serverA1}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := service.Allowed(ctx, user.ID, "Server.View", Scope{Type: "server", ID: ptr("sa1")}); err != nil || !allowed {
		t.Fatalf("Allowed(sa1) = %t, %v; want true", allowed, err)
	}
	if allowed, err := service.Allowed(ctx, user.ID, "Server.View", Scope{Type: "server", ID: ptr("sa2")}); err != nil || allowed {
		t.Fatalf("Allowed(sa2) = %t, %v; want false (sibling server, same tenant, no grant)", allowed, err)
	}
}

// TestTenantMembershipAloneGrantsNoPermission is the explicit test demanded
// by GameNode_Tenant_Foundation_Prompt.md section 3.6 and this step's item
// 10: belonging to a tenant (internal/tenants.Membership) never by itself
// makes any permission effective. Only a role assignment does.
func TestTenantMembershipAloneGrantsNoPermission(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	alice, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "member-only", Email: "member-only@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	insertRBACServerForTenant(t, db, "member-only-server", tenantA.ID)
	if _, err = tenants.New(db).AddMember(ctx, tenantA.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	service := New(db)
	allowed, err := service.Allowed(ctx, alice.ID, "Server.View", Scope{Type: "server", ID: ptr("member-only-server")})
	if err != nil || allowed {
		t.Fatalf("Allowed(tenant member without a role assignment) = %t, %v; want false", allowed, err)
	}
	allowed, err = service.Allowed(ctx, alice.ID, "Server.View", Scope{Type: "tenant", ID: &tenantA.ID})
	if err != nil || allowed {
		t.Fatalf("Allowed(tenant member without a role assignment, tenant scope) = %t, %v; want false", allowed, err)
	}
}

// TestTenantScopeDisabledUserDeniedBeforeAdminBypass and
// TestTenantScopeAdminBypassesEvaluator mirror the existing global/server
// disabled-user and admin-bypass coverage for the new tenant scope.
func TestTenantScopeDisabledUserDeniedBeforeAdminBypass(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := auth.New(db).CreateInitialAdmin(ctx, "admin", "admin@example.test", "a password long enough"); err != nil {
		t.Fatal(err)
	}
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "disabled-tenant", Email: "disabled-tenant@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	service := New(db)
	role, err := service.CreateRole(ctx, "Tenant Role", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err = identities.UpdateUser(ctx, "", user.ID, identity.UpdateUserInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := service.Allowed(ctx, user.ID, "Server.View", Scope{Type: "tenant", ID: &tenantA.ID}); err != nil || allowed {
		t.Fatalf("Allowed(disabled user, tenant scope) = %t, %v; want false", allowed, err)
	}
}
func TestTenantScopeAdminBypassesEvaluator(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	admin, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "tenant-admin", Email: "tenant-admin@example.test", Password: "a password long enough", IsAdmin: true})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	insertRBACServerForTenant(t, db, "admin-bypass-server", tenantA.ID)
	service := New(db)
	// No role assignment of any kind: an enabled admin bypasses the
	// evaluator entirely, at every scope type.
	if allowed, err := service.Allowed(ctx, admin.ID, "Server.View", Scope{Type: "tenant", ID: &tenantA.ID}); err != nil || !allowed {
		t.Fatalf("Allowed(admin, tenant scope) = %t, %v; want true", allowed, err)
	}
	if allowed, err := service.Allowed(ctx, admin.ID, "Server.View", Scope{Type: "server", ID: ptr("admin-bypass-server")}); err != nil || !allowed {
		t.Fatalf("Allowed(admin, server scope) = %t, %v; want true", allowed, err)
	}
}

// TestMixedOrGlobalOnlyRoleRejectsTenantAssignment covers item 11's
// "Mixed/global-only role: tenant assignment rejected" and the tenant
// equivalents of the existing ErrEmptyServerRole/ErrInvalidScope guards.
func TestMixedOrGlobalOnlyRoleRejectsTenantAssignment(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "mixed-tenant", Email: "mixed-tenant@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	service := New(db)

	globalOnlyRole, err := service.CreateRole(ctx, "Global Only", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, globalOnlyRole.ID, []string{"Users.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, globalOnlyRole.ID, Scope{Type: "tenant", ID: &tenantA.ID}); !errors.Is(err, ErrInvalidTenantScope) {
		t.Fatalf("global-only role tenant assignment error = %v, want ErrInvalidTenantScope", err)
	}

	mixedRole, err := service.CreateRole(ctx, "Mixed", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, mixedRole.ID, []string{"Server.View", "Users.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, mixedRole.ID, Scope{Type: "tenant", ID: &tenantA.ID}); !errors.Is(err, ErrInvalidTenantScope) {
		t.Fatalf("mixed role tenant assignment error = %v, want ErrInvalidTenantScope", err)
	}

	emptyRole, err := service.CreateRole(ctx, "Empty For Tenant", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, emptyRole.ID, Scope{Type: "tenant", ID: &tenantA.ID}); !errors.Is(err, ErrEmptyTenantRole) {
		t.Fatalf("empty role tenant assignment error = %v, want ErrEmptyTenantRole", err)
	}

	// A role suitable for tenant/server scope is still rejected when the
	// named tenant does not exist.
	suitable, err := service.CreateRole(ctx, "Suitable", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, suitable.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, suitable.ID, Scope{Type: "tenant", ID: ptr("does-not-exist")}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown tenant assignment error = %v, want sql.ErrNoRows", err)
	}
}

// TestServerCreateSupportsGlobalAndTenantButNotServerScope covers this
// step's item 5: Server.Create is assignable at global or tenant scope, and
// explicitly rejected at server scope (a server does not exist yet at the
// moment Server.Create is evaluated).
func TestServerCreateSupportsGlobalAndTenantButNotServerScope(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	globalUser, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "create-global", Email: "create-global@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantUser, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "create-tenant", Email: "create-tenant@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	tenantB := createRBACTenant(t, db, "Tenant B")
	insertRBACServerForTenant(t, db, "create-scope-server", tenantA.ID)
	service := New(db)
	role, err := service.CreateRole(ctx, "Creator", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.Create"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, globalUser.ID, role.ID, Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, tenantUser.ID, role.ID, Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	// Server scope is rejected outright by the evaluator's scope catalog
	// check, so this cannot be created as an assignment in the first place.
	if err = service.AssignUser(ctx, tenantUser.ID, role.ID, Scope{Type: "server", ID: ptr("create-scope-server")}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("Server.Create server-scope assignment error = %v, want ErrInvalidScope", err)
	}
	if allowed, err := service.Allowed(ctx, globalUser.ID, "Server.Create", Scope{Type: "tenant", ID: &tenantA.ID}); err != nil || !allowed {
		t.Fatalf("global grantee Server.Create at tenant A = %t, %v; want true", allowed, err)
	}
	if allowed, err := service.Allowed(ctx, globalUser.ID, "Server.Create", Scope{Type: "tenant", ID: &tenantB.ID}); err != nil || !allowed {
		t.Fatalf("global grantee Server.Create at tenant B = %t, %v; want true", allowed, err)
	}
	if allowed, err := service.Allowed(ctx, tenantUser.ID, "Server.Create", Scope{Type: "tenant", ID: &tenantA.ID}); err != nil || !allowed {
		t.Fatalf("tenant grantee Server.Create at tenant A = %t, %v; want true", allowed, err)
	}
	if allowed, err := service.Allowed(ctx, tenantUser.ID, "Server.Create", Scope{Type: "tenant", ID: &tenantB.ID}); err != nil || allowed {
		t.Fatalf("tenant grantee Server.Create at tenant B = %t, %v; want false", allowed, err)
	}
	// Evaluating Server.Create "for a server" makes no sense and is
	// rejected by the evaluator too, independent of assignment validation.
	if allowed, err := service.Allowed(ctx, globalUser.ID, "Server.Create", Scope{Type: "server", ID: ptr("create-scope-server")}); err != nil || allowed {
		t.Fatalf("Server.Create at server scope = %t, %v; want false", allowed, err)
	}
}

func TestRoleTenantAssignableSemantics(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	service := New(db)
	tests := []struct {
		name        string
		permissions []string
		want        bool
	}{
		{name: "empty", permissions: nil, want: false},
		{name: "tenant permissions", permissions: []string{"Server.View", "Server.Create", "Console.View"}, want: true},
		{name: "global only", permissions: []string{"Users.View"}, want: false},
		{name: "mixed", permissions: []string{"Users.View", "Server.View"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, err := service.CreateRole(ctx, "tenant-role-"+strings.ReplaceAll(tt.name, " ", "-"), "")
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
			if role.TenantAssignable != tt.want {
				t.Fatalf("tenant_assignable = %t, want %t (%v)", role.TenantAssignable, tt.want, role.Permissions)
			}
			// server_assignable must stay independently correct: Server.Create
			// alone is tenant-assignable but not server-assignable.
			if tt.name == "tenant permissions" {
				soloCreate, err := service.CreateRole(ctx, "solo-create", "")
				if err != nil {
					t.Fatal(err)
				}
				if err = service.ReplacePermissions(ctx, soloCreate.ID, []string{"Server.Create"}); err != nil {
					t.Fatal(err)
				}
				soloCreate, err = service.GetRole(ctx, soloCreate.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !soloCreate.TenantAssignable || soloCreate.ServerAssignable {
					t.Fatalf("Server.Create-only role: tenant_assignable=%t server_assignable=%t, want true/false", soloCreate.TenantAssignable, soloCreate.ServerAssignable)
				}
			}
		})
	}
}

// TestRolePermissionUpdateCannotInvalidateTenantAssignment mirrors
// TestRolePermissionUpdateCannotInvalidateServerAssignment for the tenant
// scope guard added to ReplacePermissions.
func TestRolePermissionUpdateCannotInvalidateTenantAssignment(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "tenant-assigned", Email: "tenant-assigned@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	service := New(db)
	role, err := service.CreateRole(ctx, "Assigned Tenant Operator", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View", "Users.View"}); !errors.Is(err, ErrRoleHasTenantAssignments) {
		t.Fatalf("mixed permission update error = %v, want ErrRoleHasTenantAssignments", err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, nil); !errors.Is(err, ErrRoleHasTenantAssignments) {
		t.Fatalf("empty permission update error = %v, want ErrRoleHasTenantAssignments", err)
	}
	permissions, err := service.GetRolePermissions(ctx, role.ID)
	if err != nil || len(permissions) != 1 || permissions[0] != "Server.View" {
		t.Fatalf("permissions changed after rejected update: %v, %v", permissions, err)
	}
	// A permission set that stays tenant-assignable is still accepted.
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View", "Console.View"}); err != nil {
		t.Fatalf("tenant-suitable update rejected: %v", err)
	}
}

// TestTenantManagementPermissionsAreGlobalOnlyAndIndependent covers item 7:
// Tenants.View/Tenants.Manage are global-only, Manage does not imply View,
// and tenant membership/entity administration is distinct from RBAC access
// to resources inside a tenant.
func TestTenantManagementPermissionsAreGlobalOnlyAndIndependent(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	manager, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "tenant-manager", Email: "tenant-manager@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	service := New(db)
	role, err := service.CreateRole(ctx, "Tenant Manager", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Tenants.Manage"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, manager.ID, role.ID, Scope{Type: "tenant", ID: &tenantA.ID}); !errors.Is(err, ErrInvalidTenantScope) {
		t.Fatalf("Tenants.Manage tenant-scope assignment error = %v, want ErrInvalidTenantScope", err)
	}
	if err = service.AssignUser(ctx, manager.ID, role.ID, Scope{Type: "global"}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := service.Allowed(ctx, manager.ID, "Tenants.Manage", Scope{Type: "global"}); err != nil || !allowed {
		t.Fatalf("Allowed(Tenants.Manage) = %t, %v; want true", allowed, err)
	}
	if allowed, err := service.Allowed(ctx, manager.ID, "Tenants.View", Scope{Type: "global"}); err != nil || allowed {
		t.Fatalf("Allowed(Tenants.View) = %t, %v; want false (Manage does not imply View)", allowed, err)
	}
	// Tenants.Manage grants no access to resources inside the tenant it
	// administers.
	insertRBACServerForTenant(t, db, "tenant-manager-server", tenantA.ID)
	if allowed, err := service.Allowed(ctx, manager.ID, "Server.View", Scope{Type: "server", ID: ptr("tenant-manager-server")}); err != nil || allowed {
		t.Fatalf("Allowed(Tenants.Manage grantee, Server.View) = %t, %v; want false", allowed, err)
	}
}

func insertRBACServer(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	insertRBACServerForTenant(t, db, id, tenants.DefaultTenantID)
}
func insertRBACServerForTenant(t *testing.T, db *sql.DB, id, tenantID string) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO servers(id,tenant_id,name,description,creation_mode,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", id, tenantID, id, "", "custom", "C:/", "test.exe", "[]", "{}", "native", 0, "never", "terminate", "", 15, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}
func createRBACTenant(t *testing.T, db *sql.DB, name string) tenants.Tenant {
	t.Helper()
	tenant, err := tenants.New(db).Create(context.Background(), tenants.CreateInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return tenant
}

// TestListTenantAssignmentsMirrorsListServerAssignments proves the tenant
// access read (used by GET /api/v1/tenants/{id}/access) reuses the existing
// assignment tables exactly like ListServerAssignments does for server
// scope: it returns both direct user and group assignments for the
// requested tenant, and nothing for an unrelated tenant.
func TestListTenantAssignmentsMirrorsListServerAssignments(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	identities := identity.New(db)
	user, err := identities.CreateUser(ctx, identity.CreateUserInput{Username: "direct-tenant", Email: "direct-tenant@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := identities.CreateGroup(ctx, identity.CreateGroupInput{Name: "tenant-operators"})
	if err != nil {
		t.Fatal(err)
	}
	tenantA := createRBACTenant(t, db, "Tenant A")
	tenantB := createRBACTenant(t, db, "Tenant B")
	service := New(db)
	role, err := service.CreateRole(ctx, "Tenant Access Role", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ReplacePermissions(ctx, role.ID, []string{"Server.View"}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignGroup(ctx, group.ID, role.ID, Scope{Type: "tenant", ID: &tenantA.ID}); err != nil {
		t.Fatal(err)
	}
	assignments, err := service.ListTenantAssignments(ctx, tenantA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 2 {
		t.Fatalf("tenant A assignments = %#v, want 2", assignments)
	}
	foundUser, foundGroup := false, false
	for _, assignment := range assignments {
		if assignment.Scope.Type != "tenant" || assignment.Scope.ID == nil || *assignment.Scope.ID != tenantA.ID {
			t.Fatalf("unexpected assignment scope: %#v", assignment)
		}
		switch assignment.SubjectType {
		case "user":
			foundUser = assignment.SubjectID == user.ID && assignment.SubjectName == user.Username
		case "group":
			foundGroup = assignment.SubjectID == group.ID && assignment.SubjectName == group.Name
		}
	}
	if !foundUser || !foundGroup {
		t.Fatalf("missing expected subjects: %#v", assignments)
	}
	empty, err := service.ListTenantAssignments(ctx, tenantB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("tenant B assignments = %#v, want none", empty)
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
