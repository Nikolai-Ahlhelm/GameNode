package rbac

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
)

var ErrUnknownPermission = errors.New("unknown permission")
var ErrDuplicateAssignment = errors.New("duplicate role assignment")
var ErrInvalidScope = errors.New("role contains permissions that cannot be assigned at server scope")
var ErrEmptyServerRole = errors.New("role has no permissions and cannot be assigned at server scope")
var ErrRoleHasServerAssignments = errors.New("role has server-scoped assignments and must remain server-assignable")

var roleNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]*$`)

type Scope struct {
	Type string  `json:"scope_type"`
	ID   *string `json:"scope_id,omitempty"`
}
type Role struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	Permissions          []string  `json:"permissions,omitempty"`
	ServerAssignable     bool      `json:"server_assignable"`
	CreatedAt, UpdatedAt time.Time `json:"-"`
}
type Assignment struct {
	ID       string `json:"id"`
	RoleID   string `json:"role_id"`
	RoleName string `json:"role_name"`
	Scope    Scope  `json:"scope"`
}
type SubjectAssignment struct {
	Assignment
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	SubjectName string `json:"subject_name"`
}
type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }
func scope(s Scope) error {
	if s.Type == "global" && s.ID == nil {
		return nil
	}
	if s.Type == "server" && s.ID != nil && *s.ID != "" {
		return nil
	}
	return errors.New("scope must be global or a specific server")
}
func (s *Service) CreateRole(c context.Context, name, description string) (Role, error) {
	name, e := normalizeRoleName(name)
	if e != nil {
		return Role{}, e
	}
	n := s.now().UTC()
	r := Role{ID: id(), Name: name, Description: strings.TrimSpace(description), Permissions: []string{}, ServerAssignable: false, CreatedAt: n, UpdatedAt: n}
	_, e = s.db.ExecContext(c, "INSERT INTO roles(id,name,description,created_at,updated_at) VALUES(?,?,?,?,?)", r.ID, r.Name, r.Description, ts(n), ts(n))
	return r, e
}
func (s *Service) ListRoles(c context.Context) ([]Role, error) {
	rows, e := s.db.QueryContext(c, "SELECT id,name,description,created_at,updated_at FROM roles ORDER BY name COLLATE NOCASE")
	if e != nil {
		return nil, e
	}
	var out []Role
	for rows.Next() {
		var r Role
		var a, b string
		if e = rows.Scan(&r.ID, &r.Name, &r.Description, &a, &b); e != nil {
			return nil, e
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, a)
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, b)
		out = append(out, r)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	for index := range out {
		if out[index].Permissions, e = s.GetRolePermissions(c, out[index].ID); e != nil {
			return nil, e
		}
		out[index].ServerAssignable = ServerAssignable(out[index].Permissions)
	}
	return out, nil
}
func (s *Service) GetRole(c context.Context, role string) (Role, error) {
	var r Role
	var a, b string
	e := s.db.QueryRowContext(c, "SELECT id,name,description,created_at,updated_at FROM roles WHERE id=?", role).Scan(&r.ID, &r.Name, &r.Description, &a, &b)
	if e != nil {
		return r, e
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, a)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, b)
	r.Permissions, e = s.getRolePermissions(c, role)
	if e != nil {
		return Role{}, e
	}
	r.ServerAssignable = ServerAssignable(r.Permissions)
	return r, nil
}
func (s *Service) UpdateRole(c context.Context, role, name, description string) (Role, error) {
	name, e := normalizeRoleName(name)
	if e != nil {
		return Role{}, e
	}
	_, e = s.db.ExecContext(c, "UPDATE roles SET name=?,description=?,updated_at=? WHERE id=?", name, strings.TrimSpace(description), ts(s.now()), role)
	if e != nil {
		return Role{}, e
	}
	return s.GetRole(c, role)
}

func normalizeRoleName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 64 || !roleNamePattern.MatchString(value) {
		return "", errors.New("role name must be 2 to 64 ASCII letters, digits, spaces, dots, hyphens, or underscores")
	}
	return value, nil
}
func (s *Service) DeleteRole(c context.Context, role string) error {
	r, e := s.db.ExecContext(c, "DELETE FROM roles WHERE id=?", role)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Service) GetRolePermissions(c context.Context, role string) ([]string, error) {
	var exists int
	if e := s.db.QueryRowContext(c, "SELECT COUNT(*) FROM roles WHERE id=?", role).Scan(&exists); e != nil {
		return nil, e
	}
	if exists == 0 {
		return nil, sql.ErrNoRows
	}
	return s.getRolePermissions(c, role)
}

func (s *Service) getRolePermissions(c context.Context, role string) ([]string, error) {
	rows, e := s.db.QueryContext(c, "SELECT permission_key FROM role_permissions WHERE role_id=? ORDER BY permission_key", role)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if e = rows.Scan(&k); e != nil {
			return nil, e
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
func (s *Service) ReplacePermissions(c context.Context, role string, keys []string) error {
	for _, k := range keys {
		if !Known(k) {
			return fmt.Errorf("%w: %s", ErrUnknownPermission, k)
		}
	}
	tx, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRowContext(c, "SELECT COUNT(*) FROM roles WHERE id=?", role).Scan(&n); e != nil || n == 0 {
		return sql.ErrNoRows
	}
	if !ServerAssignable(keys) {
		if e = tx.QueryRowContext(c, `SELECT EXISTS(
			SELECT 1 FROM user_role_assignments WHERE role_id=? AND scope_type='server'
			UNION ALL
			SELECT 1 FROM group_role_assignments WHERE role_id=? AND scope_type='server'
		)`, role, role).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return ErrRoleHasServerAssignments
		}
	}
	if _, e = tx.ExecContext(c, "DELETE FROM role_permissions WHERE role_id=?", role); e != nil {
		return e
	}
	for _, k := range keys {
		if _, e = tx.ExecContext(c, "INSERT INTO role_permissions(role_id,permission_key) VALUES(?,?)", role, k); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Service) AssignUser(c context.Context, user, role string, sc Scope) error {
	return s.assign(c, "user_role_assignments", "user_id", user, role, sc)
}
func (s *Service) AssignGroup(c context.Context, g, role string, sc Scope) error {
	return s.assign(c, "group_role_assignments", "group_id", g, role, sc)
}
func (s *Service) assign(c context.Context, table, col, subject, role string, sc Scope) error {
	if e := scope(sc); e != nil {
		return e
	}
	var n int
	if e := s.db.QueryRowContext(c, "SELECT COUNT(*) FROM "+map[string]string{"user_role_assignments": "users", "group_role_assignments": "groups"}[table]+" WHERE id=?", subject).Scan(&n); e != nil || n == 0 {
		return sql.ErrNoRows
	}
	if e := s.db.QueryRowContext(c, "SELECT COUNT(*) FROM roles WHERE id=?", role).Scan(&n); e != nil || n == 0 {
		return sql.ErrNoRows
	}
	if sc.Type == "server" {
		permissions, e := s.GetRolePermissions(c, role)
		if e != nil {
			return e
		}
		if len(permissions) == 0 {
			return ErrEmptyServerRole
		}
		if !ServerAssignable(permissions) {
			return ErrInvalidScope
		}
	}
	if sc.Type == "server" {
		if e := s.db.QueryRowContext(c, "SELECT COUNT(*) FROM servers WHERE id=?", *sc.ID).Scan(&n); e != nil || n == 0 {
			return sql.ErrNoRows
		}
	}
	_, e := s.db.ExecContext(c, "INSERT INTO "+table+"(id,"+col+",role_id,scope_type,scope_id) VALUES(?,?,?,?,?)", id(), subject, role, sc.Type, sc.ID)
	if e != nil && strings.Contains(e.Error(), "constraint") {
		return ErrDuplicateAssignment
	}
	return e
}

// ListServerAssignments exposes the existing assignment tables in a server
// context; it does not compute or persist effective permissions.
func (s *Service) ListServerAssignments(c context.Context, server string) ([]SubjectAssignment, error) {
	const q = `SELECT a.id,a.role_id,r.name,a.scope_type,a.scope_id,'user',u.id,u.username FROM user_role_assignments a JOIN roles r ON r.id=a.role_id JOIN users u ON u.id=a.user_id WHERE a.scope_type='server' AND a.scope_id=? UNION ALL SELECT a.id,a.role_id,r.name,a.scope_type,a.scope_id,'group',g.id,g.name FROM group_role_assignments a JOIN roles r ON r.id=a.role_id JOIN groups g ON g.id=a.group_id WHERE a.scope_type='server' AND a.scope_id=? ORDER BY 8 COLLATE NOCASE`
	rows, e := s.db.QueryContext(c, q, server, server)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []SubjectAssignment
	for rows.Next() {
		var item SubjectAssignment
		var scopeID string
		if e = rows.Scan(&item.ID, &item.RoleID, &item.RoleName, &item.Scope.Type, &scopeID, &item.SubjectType, &item.SubjectID, &item.SubjectName); e != nil {
			return nil, e
		}
		item.Scope.ID = &scopeID
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Service) ListUserAssignments(c context.Context, user string) ([]Assignment, error) {
	return s.list(c, "user_role_assignments", "user_id", user)
}
func (s *Service) ListGroupAssignments(c context.Context, g string) ([]Assignment, error) {
	return s.list(c, "group_role_assignments", "group_id", g)
}
func (s *Service) list(c context.Context, table, col, subject string) ([]Assignment, error) {
	rows, e := s.db.QueryContext(c, "SELECT a.id,a.role_id,r.name,a.scope_type,a.scope_id FROM "+table+" a JOIN roles r ON r.id=a.role_id WHERE a."+col+"=? ORDER BY r.name", subject)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Assignment
	for rows.Next() {
		var a Assignment
		var sid sql.NullString
		if e = rows.Scan(&a.ID, &a.RoleID, &a.RoleName, &a.Scope.Type, &sid); e != nil {
			return nil, e
		}
		if sid.Valid {
			a.Scope.ID = &sid.String
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Service) RemoveUserAssignment(c context.Context, id string) error {
	return s.remove(c, "user_role_assignments", id)
}
func (s *Service) RemoveUserAssignmentFor(c context.Context, user, id string) error {
	return s.removeFor(c, "user_role_assignments", "user_id", user, id)
}
func (s *Service) RemoveGroupAssignment(c context.Context, id string) error {
	return s.remove(c, "group_role_assignments", id)
}
func (s *Service) RemoveGroupAssignmentFor(c context.Context, group, id string) error {
	return s.removeFor(c, "group_role_assignments", "group_id", group, id)
}
func (s *Service) removeFor(c context.Context, table, col, subject, id string) error {
	r, e := s.db.ExecContext(c, "DELETE FROM "+table+" WHERE id=? AND "+col+"=?", id, subject)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Service) remove(c context.Context, table, id string) error {
	r, e := s.db.ExecContext(c, "DELETE FROM "+table+" WHERE id=?", id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Service) Allowed(c context.Context, user, permission string, requested Scope) (bool, error) {
	if !Known(permission) {
		return false, ErrUnknownPermission
	}
	if e := scope(requested); e != nil {
		return false, e
	}
	if GlobalOnly(permission) && requested.Type != "global" {
		return false, nil
	}
	var admin, disabled int
	e := s.db.QueryRowContext(c, "SELECT is_admin,disabled FROM users WHERE id=?", user).Scan(&admin, &disabled)
	if e != nil {
		return false, e
	}
	if disabled != 0 {
		return false, nil
	}
	if admin != 0 {
		return true, nil
	}
	q := `SELECT 1 FROM role_permissions rp JOIN roles r ON r.id=rp.role_id JOIN (SELECT role_id,scope_type,scope_id FROM user_role_assignments WHERE user_id=? UNION SELECT gra.role_id,gra.scope_type,gra.scope_id FROM group_role_assignments gra JOIN group_memberships gm ON gm.group_id=gra.group_id WHERE gm.user_id=?) a ON a.role_id=r.id WHERE rp.permission_key=? AND (a.scope_type='global' OR (a.scope_type=? AND a.scope_id=?)) LIMIT 1`
	var one int
	var idv any = nil
	if requested.ID != nil {
		idv = *requested.ID
	}
	e = s.db.QueryRowContext(c, q, user, user, permission, requested.Type, idv).Scan(&one)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	return e == nil, e
}

// ServerAssignable reports whether a role definition can be used by a
// server-scoped assignment. Empty roles are deliberately excluded because
// such an assignment can never grant access. Every selected permission must
// include server in its catalog-defined allowed scopes.
func ServerAssignable(permissions []string) bool {
	if len(permissions) == 0 {
		return false
	}
	for _, permission := range permissions {
		if GlobalOnly(permission) {
			return false
		}
	}
	return true
}
func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
