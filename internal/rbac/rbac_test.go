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
func ptr(s string) *string { return &s }
