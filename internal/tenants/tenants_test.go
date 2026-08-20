package tenants_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"gamenode"
	"gamenode/internal/database"
	"gamenode/internal/tenants"
)

func testService(t *testing.T) (*tenants.Service, *sql.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	return tenants.New(db), db
}

func insertUser(t *testing.T, db *sql.DB, id, username string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO users(id,username,email,password_hash,is_admin,disabled,display_name,created_at,updated_at) VALUES(?,?,?,?,0,0,'','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, username, username+"@example.test", "hash"); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultTenantExistsAfterFreshMigration(t *testing.T) {
	svc, _ := testService(t)
	got, err := svc.Get(context.Background(), tenants.DefaultTenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name == "" || got.Slug == "" {
		t.Fatalf("default tenant missing name/slug: %#v", got)
	}
}

func TestCreateGetListUpdateTenant(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenants.CreateInput{Name: "Acme Corp"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Slug != "acme-corp" {
		t.Fatalf("unexpected created tenant: %#v", created)
	}
	got, err := svc.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != created {
		t.Fatalf("Get() = %#v, want %#v", got, created)
	}
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list {
		if item.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("created tenant missing from List()")
	}
	newName := "Acme Corporation"
	updated, err := svc.Update(ctx, created.ID, tenants.UpdateInput{Name: &newName})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != newName {
		t.Fatalf("Update() name = %q, want %q", updated.Name, newName)
	}
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("UpdatedAt went backwards: %v -> %v", created.UpdatedAt, updated.UpdatedAt)
	}
}

func TestStatusPageSettingsAndSlugLookup(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenants.CreateInput{Name: "Status Tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if created.StatusPageEnabled || created.StatusPagePublic {
		t.Fatal("new tenant status page must be disabled and private")
	}
	yes := true
	updated, err := svc.Update(ctx, created.ID, tenants.UpdateInput{StatusPageEnabled: &yes, StatusPagePublic: &yes})
	if err != nil {
		t.Fatal(err)
	}
	bySlug, err := svc.GetBySlug(ctx, "status-tenant")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.StatusPageEnabled || !updated.StatusPagePublic || bySlug.ID != created.ID {
		t.Fatalf("unexpected status settings: updated=%#v bySlug=%#v", updated, bySlug)
	}
}

func TestCreateExplicitSlugAndDuplicateRejection(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, tenants.CreateInput{Name: "Studio One", Slug: "studio-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, tenants.CreateInput{Name: "Another Name", Slug: "studio-1"}); !errors.Is(err, tenants.ErrDuplicateSlug) {
		t.Fatalf("duplicate slug error = %v, want ErrDuplicateSlug", err)
	}
	if _, err := svc.Create(ctx, tenants.CreateInput{Name: "Studio One"}); !errors.Is(err, tenants.ErrDuplicateName) {
		t.Fatalf("duplicate name error = %v, want ErrDuplicateName", err)
	}
}

func TestGetUnknownTenantReturnsNotFound(t *testing.T) {
	svc, _ := testService(t)
	if _, err := svc.Get(context.Background(), "does-not-exist"); !errors.Is(err, tenants.ErrTenantNotFound) {
		t.Fatalf("Get() error = %v, want ErrTenantNotFound", err)
	}
}

func TestUpdateUnknownTenantReturnsNotFound(t *testing.T) {
	svc, _ := testService(t)
	name := "New Name"
	if _, err := svc.Update(context.Background(), "does-not-exist", tenants.UpdateInput{Name: &name}); !errors.Is(err, tenants.ErrTenantNotFound) {
		t.Fatalf("Update() error = %v, want ErrTenantNotFound", err)
	}
}

func TestDeleteEmptyTenantSucceeds(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenants.CreateInput{Name: "Temp Tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Get(ctx, created.ID); !errors.Is(err, tenants.ErrTenantNotFound) {
		t.Fatalf("Get() after delete = %v, want ErrTenantNotFound", err)
	}
}

func TestDeleteUnknownTenantReturnsNotFound(t *testing.T) {
	svc, _ := testService(t)
	if err := svc.Delete(context.Background(), "does-not-exist"); !errors.Is(err, tenants.ErrTenantNotFound) {
		t.Fatalf("Delete() error = %v, want ErrTenantNotFound", err)
	}
}

// TestDeleteTenantWithServerRejected inserts a server row directly (avoiding
// a dependency on internal/servers from this package's tests) to prove
// Delete consults live server ownership and refuses to remove a tenant that
// still owns one, without touching the server itself.
func TestDeleteTenantWithServerRejected(t *testing.T) {
	svc, db := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenants.CreateInput{Name: "Has Servers"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO servers(id,tenant_id,creation_mode,name,working_directory,executable,created_at,updated_at) VALUES('srv',?,'custom','Srv','/tmp','exe','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, created.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Delete(ctx, created.ID); !errors.Is(err, tenants.ErrTenantHasServers) {
		t.Fatalf("Delete() error = %v, want ErrTenantHasServers", err)
	}
	var stillExists int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE id='srv'`).Scan(&stillExists); err != nil {
		t.Fatal(err)
	}
	if stillExists != 1 {
		t.Fatal("rejected tenant delete must not remove the server")
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM servers WHERE id='srv'`); err != nil {
		t.Fatal(err)
	}
	if err = svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete() after removing server = %v, want nil", err)
	}
}

func TestMembershipAddListRemove(t *testing.T) {
	svc, db := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenants.CreateInput{Name: "Membership Tenant"})
	if err != nil {
		t.Fatal(err)
	}
	insertUser(t, db, "user-1", "alice")
	if _, err = svc.AddMember(ctx, created.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	members, err := svc.ListMembers(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UserID != "user-1" || members[0].TenantID != created.ID {
		t.Fatalf("unexpected members: %#v", members)
	}
	if err = svc.RemoveMember(ctx, created.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	members, err = svc.ListMembers(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Fatalf("members after remove = %#v, want empty", members)
	}
}

func TestMembershipDuplicateRejected(t *testing.T) {
	svc, db := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenants.CreateInput{Name: "Duplicate Tenant"})
	if err != nil {
		t.Fatal(err)
	}
	insertUser(t, db, "user-1", "alice")
	if _, err = svc.AddMember(ctx, created.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AddMember(ctx, created.ID, "user-1"); !errors.Is(err, tenants.ErrDuplicateMembership) {
		t.Fatalf("duplicate membership error = %v, want ErrDuplicateMembership", err)
	}
}

func TestMembershipInvalidUserRejected(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenants.CreateInput{Name: "Invalid User Tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AddMember(ctx, created.ID, "does-not-exist"); !errors.Is(err, tenants.ErrUserNotFound) {
		t.Fatalf("AddMember with invalid user error = %v, want ErrUserNotFound", err)
	}
}

func TestMembershipInvalidTenantRejected(t *testing.T) {
	svc, db := testService(t)
	ctx := context.Background()
	insertUser(t, db, "user-1", "alice")
	if _, err := svc.AddMember(ctx, "does-not-exist", "user-1"); !errors.Is(err, tenants.ErrTenantNotFound) {
		t.Fatalf("AddMember with invalid tenant error = %v, want ErrTenantNotFound", err)
	}
	if _, err := svc.ListMembers(ctx, "does-not-exist"); !errors.Is(err, tenants.ErrTenantNotFound) {
		t.Fatalf("ListMembers with invalid tenant error = %v, want ErrTenantNotFound", err)
	}
}

func TestMembershipRemoveUnknownReturnsNotFound(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, tenants.CreateInput{Name: "Remove Tenant"})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.RemoveMember(ctx, created.ID, "does-not-exist"); !errors.Is(err, tenants.ErrMembershipNotFound) {
		t.Fatalf("RemoveMember() error = %v, want ErrMembershipNotFound", err)
	}
}

func TestMembershipAllowsSameUserInMultipleTenants(t *testing.T) {
	svc, db := testService(t)
	ctx := context.Background()
	a, err := svc.Create(ctx, tenants.CreateInput{Name: "Tenant A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create(ctx, tenants.CreateInput{Name: "Tenant B"})
	if err != nil {
		t.Fatal(err)
	}
	insertUser(t, db, "user-1", "alice")
	if _, err = svc.AddMember(ctx, a.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AddMember(ctx, b.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	membersA, err := svc.ListMembers(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	membersB, err := svc.ListMembers(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(membersA) != 1 || len(membersB) != 1 {
		t.Fatalf("expected one membership per tenant, got A=%d B=%d", len(membersA), len(membersB))
	}
}

func TestNormalizeSlugRejectsInvalidInput(t *testing.T) {
	for _, value := range []string{"", "a", "has_underscore", "-leading", "trailing-", "double--hyphen", "unïcode"} {
		if _, err := tenants.NormalizeSlug(value); err == nil {
			t.Fatalf("NormalizeSlug(%q) accepted invalid slug", value)
		}
	}
	// Uppercase input is folded to lowercase rather than rejected, matching
	// how a slug derived from a display name via slugify() is always
	// lowercase; SQLite's NOCASE uniqueness index would treat both forms as
	// the same row regardless.
	if slug, err := tenants.NormalizeSlug("Valid-Slug1"); err != nil || slug != "valid-slug1" {
		t.Fatalf("NormalizeSlug(valid) = %q, %v", slug, err)
	}
}
