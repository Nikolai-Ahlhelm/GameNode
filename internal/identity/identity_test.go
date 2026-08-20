package identity

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"gamenode"
	"gamenode/internal/auth"
	"gamenode/internal/database"
)

const password = "a password that is definitely long enough"

func newService(t *testing.T) (*Service, *auth.Service) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := auth.New(db)
	if _, err = a.CreateInitialAdmin(context.Background(), "Admin", "admin@example.test", password); err != nil {
		t.Fatal(err)
	}
	return New(db), a
}

func TestUsersAreCaseInsensitiveAndSessionsAreInvalidated(t *testing.T) {
	s, a := newService(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, CreateUserInput{Username: "Alice", DisplayName: "Alice", Email: "alice@example.test", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateUser(ctx, CreateUserInput{Username: "alice", Email: "other@example.test", Password: password}); err == nil {
		t.Fatal("case-insensitive duplicate username accepted")
	}
	if _, _, _, err = a.Login(ctx, "ALICE", password); err != nil {
		t.Fatalf("valid login: %v", err)
	}
	if _, _, _, err = a.Login(ctx, "alice", "wrong"); err == nil {
		t.Fatal("invalid password accepted")
	}
	raw, _, err := a.CreateSession(ctx, auth.User{ID: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err = s.UpdateUser(ctx, "admin", u.ID, UpdateUserInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = a.Login(ctx, "alice", password); err == nil {
		t.Fatal("disabled user logged in")
	}
	if _, _, err = a.Current(ctx, raw); err == nil {
		t.Fatal("disabled user's existing session remained valid")
	}
	enabled := true
	if _, err = s.UpdateUser(ctx, "admin", u.ID, UpdateUserInput{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	resetSession, _, err := a.CreateSession(ctx, auth.User{ID: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	newPassword := "a different password long enough"
	if err = s.ResetPassword(ctx, u.ID, newPassword); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.Current(ctx, resetSession); err == nil {
		t.Fatal("password reset did not invalidate existing session")
	}
	if _, _, _, err = a.Login(ctx, "alice", newPassword); err != nil {
		t.Fatalf("login with reset password: %v", err)
	}
	if err = s.DeleteUser(ctx, "admin", u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = a.Login(ctx, "alice", password); err == nil {
		t.Fatal("deleted user logged in")
	}
}

func TestEnsureDevelopmentAdminCreatesAndRefreshesFixedCredentials(t *testing.T) {
	s, a := newService(t)
	ctx := context.Background()

	u, err := s.EnsureDevelopmentAdmin(ctx, "dev", "dev", "dev@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !u.IsAdmin || !u.Enabled {
		t.Fatalf("development account flags = admin:%v enabled:%v", u.IsAdmin, u.Enabled)
	}
	if _, _, _, err = a.Login(ctx, "dev", "dev"); err != nil {
		t.Fatalf("fixed development credentials did not work: %v", err)
	}
	if err = s.ResetPassword(ctx, u.ID, password); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = a.Login(ctx, "dev", "dev"); err == nil {
		t.Fatal("old development password still worked")
	}
	if _, err = s.EnsureDevelopmentAdmin(ctx, "dev", "dev", "dev@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = a.Login(ctx, "dev", "dev"); err != nil {
		t.Fatalf("development credentials were not refreshed: %v", err)
	}
}

func TestIdentityDuplicateErrorsAreControlled(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	first, err := s.CreateUser(ctx, CreateUserInput{Username: "alice", Email: "alice@example.test", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateUser(ctx, CreateUserInput{Username: "ALICE", Email: "other@example.test", Password: password}); !errors.Is(err, ErrDuplicateUsername) {
		t.Fatalf("duplicate username: %v", err)
	}
	if _, err = s.CreateUser(ctx, CreateUserInput{Username: "other", Email: "ALICE@example.test", Password: password}); !errors.Is(err, ErrDuplicateEmail) {
		t.Fatalf("duplicate email: %v", err)
	}
	duplicateEmail := "ALICE@example.test"
	if _, err = s.UpdateUser(ctx, "admin", first.ID, UpdateUserInput{Email: &duplicateEmail}); err != nil {
		t.Fatalf("same user's case-only email update: %v", err)
	}
	if _, err = s.CreateGroup(ctx, CreateGroupInput{Name: "operators"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateGroup(ctx, CreateGroupInput{Name: "OPERATORS"}); !errors.Is(err, ErrDuplicateGroup) {
		t.Fatalf("duplicate group: %v", err)
	}
	if group, createErr := s.CreateGroup(ctx, CreateGroupInput{Name: "Minecraft Admins"}); createErr != nil {
		t.Fatalf("create group with display name: %v", createErr)
	} else if group.Name != "Minecraft Admins" {
		t.Fatalf("group name = %q", group.Name)
	}
}

func TestLastActiveAdminIsProtected(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	admins, err := s.ListUsers(ctx)
	if err != nil || len(admins) != 1 {
		t.Fatal(err)
	}
	admin := admins[0]
	disabled := false
	if _, err = s.UpdateUser(ctx, admin.ID, admin.ID, UpdateUserInput{Enabled: &disabled}); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("disable last admin: %v", err)
	}
	if err = s.DeleteUser(ctx, admin.ID, admin.ID); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("delete last admin: %v", err)
	}
}

func TestGroupsAndMembershipCleanup(t *testing.T) {
	s, _ := newService(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, CreateUserInput{Username: "member", Email: "member@example.test", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateGroup(ctx, CreateGroupInput{Name: "Operators", Description: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateGroup(ctx, CreateGroupInput{Name: "operators"}); err == nil {
		t.Fatal("duplicate group accepted")
	}
	name := "Operators-2"
	if g, err = s.UpdateGroup(ctx, g.ID, UpdateGroupInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if err = s.AddMember(ctx, g.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	userGroups, err := s.GroupsForUser(ctx, u.ID)
	if err != nil || len(userGroups) != 1 || userGroups[0].ID != g.ID {
		t.Fatalf("groups for user: %v %v", userGroups, err)
	}
	userSummaries, err := s.ListUserSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var groupCount int
	for _, summary := range userSummaries {
		if summary.ID == u.ID {
			if summary.GroupCount != nil {
				groupCount = *summary.GroupCount
			}
		}
	}
	if groupCount != 1 {
		t.Fatalf("group count = %d", groupCount)
	}
	groupSummaries, err := s.ListGroupSummaries(ctx)
	if err != nil || len(groupSummaries) != 1 || groupSummaries[0].MemberCount != 1 {
		t.Fatalf("group summaries: %v %v", groupSummaries, err)
	}
	if err = s.AddMember(ctx, g.ID, u.ID); !errors.Is(err, ErrDuplicateMember) {
		t.Fatalf("duplicate membership: %v", err)
	}
	members, err := s.Members(ctx, g.ID)
	if err != nil || len(members) != 1 {
		t.Fatalf("members: %v %v", members, err)
	}
	if err = s.RemoveMember(ctx, g.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.AddMember(ctx, g.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteGroup(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Members(ctx, g.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("group delete: %v", err)
	}
}

func TestMigrationPreservesExistingInitialAdminLogin(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL COLLATE NOCASE UNIQUE, email TEXT NOT NULL COLLATE NOCASE UNIQUE, password_hash TEXT NOT NULL, is_admin INTEGER NOT NULL DEFAULT 0, disabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL); CREATE TABLE sessions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, token_hash BLOB NOT NULL UNIQUE, csrf_token TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(db)
	if _, err = a.CreateInitialAdmin(context.Background(), "Admin", "admin@example.test", password); err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = a.Login(context.Background(), "admin", password); err != nil {
		t.Fatalf("existing admin login after migration: %v", err)
	}
}
