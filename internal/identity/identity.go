// Package identity manages local users and groups. Groups deliberately carry no
// authorization meaning; later RBAC work can build on this stable data model.
package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gamenode/internal/auth"
)

var (
	ErrLastActiveAdmin   = errors.New("at least one active administrator is required")
	ErrDuplicateMember   = errors.New("user is already a group member")
	ErrDuplicateUsername = errors.New("a user with this username already exists")
	ErrDuplicateEmail    = errors.New("a user with this email already exists")
	ErrDuplicateGroup    = errors.New("a group with this name already exists")
)

var groupIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]*$`)

type User struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
	Enabled     bool       `json:"enabled"`
	IsAdmin     bool       `json:"is_admin"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type UserSummary struct {
	User
	GroupCount *int `json:"group_count,omitempty"`
}

type GroupSummary struct {
	Group
	MemberCount     int  `json:"member_count"`
	AssignmentCount *int `json:"assignment_count,omitempty"`
}

type CreateUserInput struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	IsAdmin     bool   `json:"is_admin"`
}
type UpdateUserInput struct {
	Username    *string `json:"username"`
	DisplayName *string `json:"display_name"`
	Email       *string `json:"email"`
	Enabled     *bool   `json:"enabled"`
	IsAdmin     *bool   `json:"is_admin"`
}
type CreateGroupInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
type UpdateGroupInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type Service struct {
	db             *sql.DB
	now            func() time.Time
	passwordPolicy auth.PasswordPolicyProvider
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) SetPasswordPolicyProvider(provider auth.PasswordPolicyProvider) {
	s.passwordPolicy = provider
}

// NormalizeUsername accepts ASCII identifiers only. This deliberately rejects
// Unicode rather than pretending SQLite NOCASE provides Unicode normalization.
func NormalizeUsername(value string) (string, error) {
	return auth.NormalizeUsername(value)
}
func NormalizeGroupName(value string) (string, error) {
	return normalizeIdentifier(value, 2, 64, "group name")
}

func normalizeIdentifier(value string, min, max int, label string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < min || len(value) > max || !groupIdentifier.MatchString(value) {
		return "", fmt.Errorf("%s must be %d to %d ASCII letters, digits, spaces, dots, hyphens, or underscores", label, min, max)
	}
	return value, nil
}
func NormalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "@") || len(value) > 254 {
		return "", errors.New("a valid email is required")
	}
	return value, nil
}

func normalizeEmail(value string) (string, error) { return NormalizeEmail(value) }
func normalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		return "", errors.New("display name must be 128 characters or fewer")
	}
	return value, nil
}
func (s *Service) validatePassword(ctx context.Context, value string) error {
	minimum, maximum := 8, 256
	if s.passwordPolicy != nil {
		var err error
		minimum, maximum, err = s.passwordPolicy.PasswordPolicy(ctx)
		if err != nil {
			return fmt.Errorf("load password policy: %w", err)
		}
	}
	if len(value) < minimum || len(value) > maximum {
		return fmt.Errorf("password must be %d to %d characters", minimum, maximum)
	}
	return nil
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, userSelect+" ORDER BY username COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func (s *Service) ListUserSummaries(ctx context.Context) ([]UserSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT users.id,users.username,users.display_name,users.email,users.is_admin,users.disabled,users.created_at,users.updated_at,users.last_login_at,COUNT(gm.group_id) FROM users LEFT JOIN group_memberships gm ON gm.user_id=users.id GROUP BY users.id ORDER BY users.username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []UserSummary
	for rows.Next() {
		var u UserSummary
		if err := scanUserSummary(rows, &u); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func (s *Service) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	err := scanUser(s.db.QueryRowContext(ctx, userSelect+" WHERE id=?", id), &u)
	return u, err
}
func (s *Service) CreateUser(ctx context.Context, in CreateUserInput) (User, error) {
	return s.createUser(ctx, s.db, in)
}

// CreateUserTx creates a user in a caller-owned transaction, allowing
// invitation consumption and tenant membership to commit atomically.
func (s *Service) CreateUserTx(ctx context.Context, tx *sql.Tx, in CreateUserInput) (User, error) {
	return s.createUser(ctx, tx, in)
}

// EnsureDevelopmentAdmin creates or refreshes the deliberately weak local
// development account. It is intended only for the explicit -dev startup
// path; production startup never calls it. The password policy is not applied
// here because the fixed dev/dev credentials are intentionally shorter than
// the normal policy.
func (s *Service) EnsureDevelopmentAdmin(ctx context.Context, username, password, email string) (User, error) {
	username, err := auth.NormalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	if strings.TrimSpace(email) == "" {
		return User{}, errors.New("development admin email is required")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	var u User
	err = scanUser(s.db.QueryRowContext(ctx, userSelect+" WHERE username=?", username), &u)
	if errors.Is(err, sql.ErrNoRows) {
		u = User{ID: newID(), Username: username, Email: strings.TrimSpace(email), Enabled: true, IsAdmin: true, CreatedAt: now, UpdatedAt: now}
		_, err = s.db.ExecContext(ctx, `INSERT INTO users(id,username,email,password_hash,is_admin,disabled,display_name,created_at,updated_at) VALUES(?,?,?,?,1,0,'',?,?)`, u.ID, u.Username, u.Email, hash, stamp(now), stamp(now))
		if err != nil {
			return User{}, classifyConstraint(err)
		}
		return u, nil
	}
	if err != nil {
		return User{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET password_hash=?,is_admin=1,disabled=0,updated_at=? WHERE id=?`, hash, stamp(now), u.ID)
	if err != nil {
		return User{}, err
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", u.ID); err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, u.ID)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Service) createUser(ctx context.Context, exec execer, in CreateUserInput) (User, error) {
	username, err := NormalizeUsername(in.Username)
	if err != nil {
		return User{}, err
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return User{}, err
	}
	displayName, err := normalizeDisplayName(in.DisplayName)
	if err != nil {
		return User{}, err
	}
	if err = s.validatePassword(ctx, in.Password); err != nil {
		return User{}, err
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	u := User{ID: newID(), Username: username, DisplayName: displayName, Email: email, Enabled: true, IsAdmin: in.IsAdmin, CreatedAt: now, UpdatedAt: now}
	_, err = exec.ExecContext(ctx, `INSERT INTO users(id,username,email,password_hash,is_admin,disabled,display_name,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, u.ID, u.Username, u.Email, hash, u.IsAdmin, 0, u.DisplayName, stamp(now), stamp(now))
	err = classifyConstraint(err)
	return u, err
}
func (s *Service) UpdateUser(ctx context.Context, actorID, id string, in UpdateUserInput) (User, error) {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return User{}, err
	}
	if in.Username != nil {
		if u.Username, err = NormalizeUsername(*in.Username); err != nil {
			return User{}, err
		}
	}
	if in.Email != nil {
		if u.Email, err = normalizeEmail(*in.Email); err != nil {
			return User{}, err
		}
	}
	if in.DisplayName != nil {
		if u.DisplayName, err = normalizeDisplayName(*in.DisplayName); err != nil {
			return User{}, err
		}
	}
	if in.Enabled != nil {
		u.Enabled = *in.Enabled
	}
	if in.IsAdmin != nil {
		u.IsAdmin = *in.IsAdmin
	}
	if err = s.requireActiveAdmin(ctx, id, u.Enabled, u.IsAdmin); err != nil {
		return User{}, err
	}
	u.UpdatedAt = s.now().UTC()
	_, err = s.db.ExecContext(ctx, `UPDATE users SET username=?,email=?,display_name=?,is_admin=?,disabled=?,updated_at=? WHERE id=?`, u.Username, u.Email, u.DisplayName, u.IsAdmin, !u.Enabled, stamp(u.UpdatedAt), id)
	err = classifyConstraint(err)
	if err != nil {
		return User{}, err
	}
	if !u.Enabled {
		_, err = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", id)
		if err != nil {
			return User{}, err
		}
	}
	return s.GetUser(ctx, id)
}
func (s *Service) ResetPassword(ctx context.Context, id, password string) error {
	return s.resetPassword(ctx, s.db, id, password)
}

func (s *Service) ResetPasswordTx(ctx context.Context, tx *sql.Tx, id, password string) error {
	return s.resetPassword(ctx, tx, id, password)
}

func (s *Service) resetPassword(ctx context.Context, exec execer, id, password string) error {
	if err := s.validatePassword(ctx, password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	now := stamp(s.now().UTC())
	result, err := exec.ExecContext(ctx, "UPDATE users SET password_hash=?,updated_at=? WHERE id=?", hash, now, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	_, err = exec.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", id)
	return err
}
func (s *Service) DeleteUser(ctx context.Context, actorID, id string) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if err = s.requireActiveAdmin(ctx, id, false, u.IsAdmin); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM users WHERE id=?", id)
	return err
}
func (s *Service) requireActiveAdmin(ctx context.Context, targetID string, targetEnabled, targetAdmin bool) error {
	if targetEnabled && targetAdmin {
		return nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_admin=1 AND disabled=0 AND id<>?", targetID).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrLastActiveAdmin
	}
	return nil
}

func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, groupSelect+" ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []Group
	for rows.Next() {
		var g Group
		if err := scanGroup(rows, &g); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
func (s *Service) ListGroupSummaries(ctx context.Context) ([]GroupSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT groups.id,groups.name,groups.description,groups.created_at,groups.updated_at,COUNT(gm.user_id),(SELECT COUNT(*) FROM group_role_assignments gra WHERE gra.group_id=groups.id) FROM groups LEFT JOIN group_memberships gm ON gm.group_id=groups.id GROUP BY groups.id ORDER BY groups.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []GroupSummary
	for rows.Next() {
		var g GroupSummary
		if err := scanGroupSummary(rows, &g); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
func (s *Service) GetGroup(ctx context.Context, id string) (Group, error) {
	var g Group
	err := scanGroup(s.db.QueryRowContext(ctx, groupSelect+" WHERE id=?", id), &g)
	return g, err
}
func (s *Service) CreateGroup(ctx context.Context, in CreateGroupInput) (Group, error) {
	name, err := NormalizeGroupName(in.Name)
	if err != nil {
		return Group{}, err
	}
	if len(in.Description) > 512 {
		return Group{}, errors.New("description must be 512 characters or fewer")
	}
	now := s.now().UTC()
	g := Group{ID: newID(), Name: name, Description: strings.TrimSpace(in.Description), CreatedAt: now, UpdatedAt: now}
	_, err = s.db.ExecContext(ctx, "INSERT INTO groups(id,name,description,created_at,updated_at) VALUES(?,?,?,?,?)", g.ID, g.Name, g.Description, stamp(now), stamp(now))
	err = classifyConstraint(err)
	return g, err
}
func (s *Service) UpdateGroup(ctx context.Context, id string, in UpdateGroupInput) (Group, error) {
	g, err := s.GetGroup(ctx, id)
	if err != nil {
		return Group{}, err
	}
	if in.Name != nil {
		if g.Name, err = NormalizeGroupName(*in.Name); err != nil {
			return Group{}, err
		}
	}
	if in.Description != nil {
		g.Description = strings.TrimSpace(*in.Description)
		if len(g.Description) > 512 {
			return Group{}, errors.New("description must be 512 characters or fewer")
		}
	}
	g.UpdatedAt = s.now().UTC()
	_, err = s.db.ExecContext(ctx, "UPDATE groups SET name=?,description=?,updated_at=? WHERE id=?", g.Name, g.Description, stamp(g.UpdatedAt), id)
	err = classifyConstraint(err)
	if err != nil {
		return Group{}, err
	}
	return s.GetGroup(ctx, id)
}
func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM groups WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Service) Members(ctx context.Context, groupID string) ([]User, error) {
	if _, err := s.GetGroup(ctx, groupID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, userSelect+" JOIN group_memberships gm ON gm.user_id=users.id WHERE gm.group_id=? ORDER BY username COLLATE NOCASE", groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func (s *Service) GroupsForUser(ctx context.Context, userID string) ([]Group, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, groupSelect+" JOIN group_memberships gm ON gm.group_id=groups.id WHERE gm.user_id=? ORDER BY name COLLATE NOCASE", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []Group
	for rows.Next() {
		var g Group
		if err := scanGroup(rows, &g); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}
func (s *Service) AddMember(ctx context.Context, groupID, userID string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO group_memberships(user_id,group_id) VALUES(?,?)", userID, groupID)
	if isConstraint(err) {
		return ErrDuplicateMember
	}
	return err
}
func (s *Service) RemoveMember(ctx context.Context, groupID, userID string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM group_memberships WHERE group_id=? AND user_id=?", groupID, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

const userSelect = "SELECT id,username,display_name,email,is_admin,disabled,created_at,updated_at,last_login_at FROM users"
const groupSelect = "SELECT id,name,description,created_at,updated_at FROM groups"

type scanner interface{ Scan(...any) error }

func scanUser(row scanner, u *User) error {
	var admin, disabled int
	var created, updated string
	var last sql.NullString
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &admin, &disabled, &created, &updated, &last); err != nil {
		return err
	}
	u.IsAdmin = admin != 0
	u.Enabled = disabled == 0
	var err error
	u.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return err
	}
	u.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return err
	}
	if last.Valid {
		v, e := time.Parse(time.RFC3339Nano, last.String)
		if e != nil {
			return e
		}
		u.LastLoginAt = &v
	}
	return nil
}
func scanUserSummary(row scanner, u *UserSummary) error {
	var admin, disabled int
	var groupCount int
	var created, updated string
	var last sql.NullString
	if err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &admin, &disabled, &created, &updated, &last, &groupCount); err != nil {
		return err
	}
	u.GroupCount = &groupCount
	u.IsAdmin = admin != 0
	u.Enabled = disabled == 0
	var err error
	u.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return err
	}
	u.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return err
	}
	if last.Valid {
		v, e := time.Parse(time.RFC3339Nano, last.String)
		if e != nil {
			return e
		}
		u.LastLoginAt = &v
	}
	return nil
}
func scanGroup(row scanner, g *Group) error {
	var created, updated string
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &created, &updated); err != nil {
		return err
	}
	var err error
	g.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return err
	}
	g.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return err
}
func scanGroupSummary(row scanner, g *GroupSummary) error {
	var created, updated string
	var assignmentCount int
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &created, &updated, &g.MemberCount, &assignmentCount); err != nil {
		return err
	}
	g.AssignmentCount = &assignmentCount
	var err error
	g.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return err
	}
	g.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return err
}
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func isConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "constraint")
}
func classifyConstraint(err error) error {
	if !isConstraint(err) {
		return err
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "users.username"):
		return ErrDuplicateUsername
	case strings.Contains(message, "users.email"):
		return ErrDuplicateEmail
	case strings.Contains(message, "groups.name"):
		return ErrDuplicateGroup
	default:
		return err
	}
}
