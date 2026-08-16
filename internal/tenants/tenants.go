// Package tenants implements the Tenant Foundation domain: persistent
// tenants and tenant memberships. It is transport-independent, like
// internal/identity and internal/servers.
//
// A tenant is a logically separate customer/organization boundary that a
// GameNode server belongs to exactly one of (see migrations/020_tenants.sql
// and docs/architecture.md). Membership in a tenant records only that a user
// belongs to it; it grants no RBAC permission by itself. internal/rbac
// remains authoritative for what a member may actually do, and gains a
// tenant scope in a later Tenant Foundation step.
package tenants

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
)

// DefaultTenantID is the fixed, non-random identifier of the tenant that
// migrations/020_tenants.sql creates and assigns every pre-existing server
// to during upgrade. It intentionally is not generated at runtime so
// upgraded installations, this package, and its tests can all refer to the
// same well-known row without a lookup.
const DefaultTenantID = "default"

var (
	// ErrTenantNotFound is returned by Get, Update, Delete, and membership
	// operations when the tenant ID does not exist.
	ErrTenantNotFound = errors.New("tenant not found")
	// ErrDuplicateName is returned by Create/Update when another tenant
	// already has the given name (case-insensitive, matching the existing
	// users/groups uniqueness convention).
	ErrDuplicateName = errors.New("a tenant with this name already exists")
	// ErrDuplicateSlug is returned by Create/Update when another tenant
	// already has the given slug.
	ErrDuplicateSlug = errors.New("a tenant with this slug already exists")
	// ErrTenantHasServers is returned by Delete when the tenant still owns
	// one or more servers. GameNode never cascade-deletes servers or their
	// files as part of deleting a tenant.
	ErrTenantHasServers = errors.New("tenant has servers and cannot be deleted")
	// ErrUserNotFound is returned by AddMember when the user ID does not
	// exist.
	ErrUserNotFound = errors.New("user not found")
	// ErrDuplicateMembership is returned by AddMember when the user already
	// belongs to the tenant. Matching identity.Service.AddMember's
	// convention, adding an existing member is a controlled error rather
	// than a silent no-op.
	ErrDuplicateMembership = errors.New("user is already a member of this tenant")
	// ErrMembershipNotFound is returned by RemoveMember when the user is not
	// currently a member of the tenant.
	ErrMembershipNotFound = errors.New("tenant membership not found")
)

// namePattern deliberately accepts ASCII only, matching the existing
// identity.NormalizeGroupName convention rather than inventing Unicode
// normalization rules for a new identifier type.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_. -]*$`)

// slugPattern is a lowercase-ASCII, hyphen-separated identifier. Slugs are a
// display/URL convenience only; they are never used as a filesystem or
// security identifier (see docs/architecture.md).
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Tenant is a logically separate customer/organization boundary. ID is
// immutable once created.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Membership records that a user belongs to a tenant. It carries no
// authorization meaning by itself; see the package doc comment.
type Membership struct {
	TenantID  string    `json:"tenant_id"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateInput struct {
	Name string `json:"name"`
	// Slug is optional; when empty it is derived from Name using the same
	// ASCII-only rules as NormalizeSlug.
	Slug string `json:"slug"`
}

type UpdateInput struct {
	Name *string `json:"name"`
	Slug *string `json:"slug"`
}

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

// NormalizeName mirrors identity.NormalizeGroupName: an ASCII display
// identifier, 2 to 100 characters.
func NormalizeName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 100 || !namePattern.MatchString(value) {
		return "", errors.New("tenant name must be 2 to 100 ASCII letters, digits, spaces, dots, hyphens, or underscores")
	}
	return value, nil
}

// NormalizeSlug validates a lowercase ASCII, hyphen-separated identifier, 2
// to 64 characters. It never invents Unicode case-folding or transliteration
// beyond SQLite's existing ASCII-only NOCASE convention.
func NormalizeSlug(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 2 || len(value) > 64 || !slugPattern.MatchString(value) {
		return "", errors.New("tenant slug must be 2 to 64 lowercase ASCII letters, digits, or single internal hyphens")
	}
	return value, nil
}

// slugify derives a candidate slug from a display name for CreateInput
// callers that do not supply one explicitly. Non-ASCII and otherwise
// disallowed characters are dropped, never transliterated.
func slugify(name string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			pendingHyphen = false
			b.WriteRune(r)
		default:
			pendingHyphen = true
		}
	}
	return b.String()
}

func (s *Service) List(ctx context.Context) ([]Tenant, error) {
	rows, err := s.db.QueryContext(ctx, tenantSelect+" ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		if err := scanTenant(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id string) (Tenant, error) {
	var t Tenant
	err := scanTenant(s.db.QueryRowContext(ctx, tenantSelect+" WHERE id=?", id), &t)
	if errors.Is(err, sql.ErrNoRows) {
		return Tenant{}, ErrTenantNotFound
	}
	return t, err
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Tenant, error) {
	name, err := NormalizeName(in.Name)
	if err != nil {
		return Tenant{}, err
	}
	slugInput := in.Slug
	if strings.TrimSpace(slugInput) == "" {
		slugInput = slugify(name)
	}
	slug, err := NormalizeSlug(slugInput)
	if err != nil {
		return Tenant{}, err
	}
	now := s.now().UTC()
	t := Tenant{ID: newID(), Name: name, Slug: slug, CreatedAt: now, UpdatedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tenants(id,name,slug,created_at,updated_at) VALUES(?,?,?,?,?)`, t.ID, t.Name, t.Slug, stamp(now), stamp(now))
	if err = classifyConstraint(err); err != nil {
		return Tenant{}, err
	}
	return t, nil
}

func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (Tenant, error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return Tenant{}, err
	}
	if in.Name != nil {
		if t.Name, err = NormalizeName(*in.Name); err != nil {
			return Tenant{}, err
		}
	}
	if in.Slug != nil {
		if t.Slug, err = NormalizeSlug(*in.Slug); err != nil {
			return Tenant{}, err
		}
	}
	t.UpdatedAt = s.now().UTC()
	_, err = s.db.ExecContext(ctx, `UPDATE tenants SET name=?,slug=?,updated_at=? WHERE id=?`, t.Name, t.Slug, stamp(t.UpdatedAt), id)
	if err = classifyConstraint(err); err != nil {
		return Tenant{}, err
	}
	return s.Get(ctx, id)
}

// Delete refuses to remove a tenant that still owns any server. It never
// deletes servers or their files, and never touches unrelated tenants.
// Memberships are removed by the tenant_memberships foreign key's ON DELETE
// CASCADE, which is a controlled cleanup of pure membership rows, not of
// servers or RBAC assignments.
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	var serverCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE tenant_id=?`, id).Scan(&serverCount); err != nil {
		return err
	}
	if serverCount > 0 {
		return ErrTenantHasServers
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM tenants WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// ListMembers returns the memberships of an existing tenant, oldest first.
func (s *Service) ListMembers(ctx context.Context, tenantID string) ([]Membership, error) {
	if _, err := s.Get(ctx, tenantID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id,user_id,created_at FROM tenant_memberships WHERE tenant_id=? ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := scanMembership(rows, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMember adds a user to a tenant. It never grants RBAC permission; see
// the package doc comment. Adding an already-existing member is a
// controlled error, matching identity.Service.AddMember's non-idempotent
// convention rather than silently succeeding.
func (s *Service) AddMember(ctx context.Context, tenantID, userID string) (Membership, error) {
	if _, err := s.Get(ctx, tenantID); err != nil {
		return Membership{}, err
	}
	var userExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, userID).Scan(&userExists); err != nil {
		return Membership{}, err
	}
	if userExists == 0 {
		return Membership{}, ErrUserNotFound
	}
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_memberships(tenant_id,user_id,created_at) VALUES(?,?,?)`, tenantID, userID, stamp(now))
	if isConstraint(err) {
		return Membership{}, ErrDuplicateMembership
	}
	if err != nil {
		return Membership{}, err
	}
	return Membership{TenantID: tenantID, UserID: userID, CreatedAt: now}, nil
}

// RemoveMember removes a user from a tenant. A nonexistent membership -
// whether because the tenant, user, or membership itself does not exist -
// is reported the same way, matching identity.Service.RemoveMember's
// existing convention.
func (s *Service) RemoveMember(ctx context.Context, tenantID, userID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM tenant_memberships WHERE tenant_id=? AND user_id=?`, tenantID, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrMembershipNotFound
	}
	return nil
}

const tenantSelect = "SELECT id,name,slug,created_at,updated_at FROM tenants"

type scanner interface{ Scan(...any) error }

func scanTenant(row scanner, t *Tenant) error {
	var created, updated string
	if err := row.Scan(&t.ID, &t.Name, &t.Slug, &created, &updated); err != nil {
		return err
	}
	var err error
	if t.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return err
	}
	t.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return err
}

func scanMembership(row scanner, m *Membership) error {
	var created string
	if err := row.Scan(&m.TenantID, &m.UserID, &created); err != nil {
		return err
	}
	var err error
	m.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
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
	case strings.Contains(message, "tenants.name"):
		return ErrDuplicateName
	case strings.Contains(message, "tenants.slug"):
		return ErrDuplicateSlug
	default:
		return err
	}
}
