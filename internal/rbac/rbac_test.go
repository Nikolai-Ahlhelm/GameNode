package rbac

import (
	"context"
	"errors"
	"gamenode"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"testing"
)

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
	platformPermissions := []string{"Users.View", "Users.Manage", "Groups.View", "Groups.Manage", "Roles.View", "Roles.Manage"}
	if err = service.ReplacePermissions(ctx, role.ID, platformPermissions); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO servers(id,name,description,creation_mode,working_directory,executable,arguments_json,environment_json,runtime_type,auto_start,restart_policy,stop_method,stop_command,stop_timeout_seconds,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", "server-a", "server", "", "custom", "C:/", "test.exe", "[]", "{}", "native", 0, "never", "terminate", "", 15, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignUser(ctx, user.ID, role.ID, Scope{Type: "server", ID: ptr("server-a")}); err != nil {
		t.Fatal(err)
	}
	if err = service.AssignGroup(ctx, group.ID, role.ID, Scope{Type: "server", ID: ptr("server-a")}); err != nil {
		t.Fatal(err)
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
func mustFirstAssignment(t *testing.T, service *Service, ctx context.Context, user string) string {
	t.Helper()
	assignments, err := service.ListUserAssignments(ctx, user)
	if err != nil || len(assignments) < 2 {
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
