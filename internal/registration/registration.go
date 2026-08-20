// Package registration implements invitation-only tenant registration.
package registration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"gamenode/internal/identity"
)

var (
	ErrForbidden          = errors.New("invitation management is forbidden")
	ErrQuotaExceeded      = errors.New("tenant user quota exceeded")
	ErrAlreadyInvited     = errors.New("an invitation is already pending")
	ErrInvitationNotFound = errors.New("invitation not found")
	ErrInvitationExpired  = errors.New("invitation expired")
	ErrInvitationConsumed = errors.New("invitation already used")
	ErrInvalidToken       = errors.New("invalid invitation token")
	ErrTooManyAttempts    = errors.New("too many invitation attempts")
	ErrAlreadyRegistered  = errors.New("email is already registered")
)

const defaultLifetime = 72 * time.Hour

type Sender interface {
	SendRegistrationInvitation(context.Context, string, string, string, time.Duration) error
}
type Service struct {
	db         *sql.DB
	identities *identity.Service
	sender     Sender
	now        func() time.Time
}

func New(db *sql.DB, ids *identity.Service, sender Sender) *Service {
	return &Service{db: db, identities: ids, sender: sender, now: time.Now}
}

type Invitation struct {
	ID, TenantID, Email string
	ExpiresAt           time.Time
}
type Preview struct {
	Invitation
	TenantName, TenantSlug string
}
type RegisterInput struct{ InvitationID, Token, Username, DisplayName, Password string }

func normalizeEmail(v string) (string, error) {
	v = strings.TrimSpace(v)
	a, e := mail.ParseAddress(v)
	if e != nil || a.Address != v {
		return "", errors.New("a valid email is required")
	}
	return strings.ToLower(v), nil
}
func tokenPair() (string, []byte, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", nil, e
	}
	t := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(t))
	return t, h[:], nil
}
func newID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (s *Service) Invite(ctx context.Context, tenantID, actorID, email, linkBase string, permissionGranted bool) (Invitation, error) {
	email, e := normalizeEmail(email)
	if e != nil {
		return Invitation{}, e
	}
	var owner string
	var quota int
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(owner_user_id,''),user_quota,name FROM tenants WHERE id=?`, tenantID).Scan(&owner, &quota, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, ErrInvitationNotFound
	}
	if err != nil {
		return Invitation{}, err
	}
	if !permissionGranted && owner != actorID {
		return Invitation{}, ErrForbidden
	}
	var exists int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email=? COLLATE NOCASE`, email).Scan(&exists); err != nil {
		return Invitation{}, err
	}
	if exists > 0 {
		return Invitation{}, ErrAlreadyRegistered
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM tenant_memberships WHERE tenant_id=?)+(SELECT COUNT(*) FROM tenant_invitations WHERE tenant_id=? AND consumed_at IS NULL AND expires_at>?)`, tenantID, tenantID, stamp(s.now())).Scan(&count); err != nil {
		return Invitation{}, err
	}
	if quota > 0 && count >= quota {
		return Invitation{}, ErrQuotaExceeded
	}
	var pending int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenant_invitations WHERE tenant_id=? AND email=? AND consumed_at IS NULL AND expires_at>?`, tenantID, email, stamp(s.now())).Scan(&pending); err != nil {
		return Invitation{}, err
	}
	if pending > 0 {
		return Invitation{}, ErrAlreadyInvited
	}
	tok, hash, e := tokenPair()
	if e != nil {
		return Invitation{}, e
	}
	now := s.now().UTC()
	inv := Invitation{ID: newID(), TenantID: tenantID, Email: email, ExpiresAt: now.Add(defaultLifetime)}
	_, e = s.db.ExecContext(ctx, `INSERT INTO tenant_invitations(id,tenant_id,email,token_hash,invited_by,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`, inv.ID, tenantID, email, hash, actorID, stamp(inv.ExpiresAt), stamp(now))
	if e != nil {
		return Invitation{}, e
	}
	link := strings.TrimRight(linkBase, "/") + "/register?invitation_id=" + url.QueryEscape(inv.ID) + "&token=" + url.QueryEscape(tok)
	if s.sender == nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM tenant_invitations WHERE id=?`, inv.ID)
		return Invitation{}, errors.New("email delivery is unavailable")
	}
	if e = s.sender.SendRegistrationInvitation(ctx, email, name, link, defaultLifetime); e != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM tenant_invitations WHERE id=?`, inv.ID)
		return Invitation{}, e
	}
	return inv, nil
}

func (s *Service) Preview(ctx context.Context, id, token string) (Preview, error) {
	var p Preview
	var hash []byte
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT i.tenant_id,i.email,i.token_hash,i.expires_at,t.name,t.slug FROM tenant_invitations i JOIN tenants t ON t.id=i.tenant_id WHERE i.id=?`, id).Scan(&p.TenantID, &p.Email, &hash, &exp, &p.TenantName, &p.TenantSlug)
	if errors.Is(err, sql.ErrNoRows) {
		return Preview{}, ErrInvitationNotFound
	}
	if err != nil {
		return Preview{}, err
	}
	if !validHash(hash, token) {
		return Preview{}, ErrInvalidToken
	}
	p.ID = id
	p.ExpiresAt, _ = time.Parse(time.RFC3339Nano, exp)
	if !p.ExpiresAt.After(s.now()) {
		return Preview{}, ErrInvitationExpired
	}
	return p, nil
}
func validHash(hash []byte, token string) bool {
	h := sha256.Sum256([]byte(token))
	return len(hash) == len(h) && subtle.ConstantTimeCompare(hash, h[:]) == 1
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (identity.User, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return identity.User{}, e
	}
	defer tx.Rollback()
	var tenantID, email string
	var hash []byte
	var exp string
	var consumed sql.NullString
	var attempts int
	var quota int
	e = tx.QueryRowContext(ctx, `SELECT i.tenant_id,i.email,i.token_hash,i.expires_at,i.consumed_at,i.attempts,t.user_quota FROM tenant_invitations i JOIN tenants t ON t.id=i.tenant_id WHERE i.id=?`, in.InvitationID).Scan(&tenantID, &email, &hash, &exp, &consumed, &attempts, &quota)
	if errors.Is(e, sql.ErrNoRows) {
		return identity.User{}, ErrInvitationNotFound
	}
	if e != nil {
		return identity.User{}, e
	}
	if consumed.Valid {
		return identity.User{}, ErrInvitationConsumed
	}
	if !s.now().Before(parse(exp)) {
		return identity.User{}, ErrInvitationExpired
	}
	if attempts >= 5 {
		return identity.User{}, ErrTooManyAttempts
	}
	if !validHash(hash, in.Token) {
		_, _ = tx.ExecContext(ctx, `UPDATE tenant_invitations SET attempts=attempts+1 WHERE id=?`, in.InvitationID)
		_ = tx.Commit()
		return identity.User{}, ErrInvalidToken
	}
	var exists int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email=? COLLATE NOCASE`, email).Scan(&exists); e != nil {
		return identity.User{}, e
	}
	if exists > 0 {
		return identity.User{}, ErrAlreadyRegistered
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM tenant_memberships WHERE tenant_id=?)+(SELECT COUNT(*) FROM tenant_invitations WHERE tenant_id=? AND consumed_at IS NULL AND expires_at>? AND id<>?)`, tenantID, tenantID, stamp(s.now()), in.InvitationID).Scan(&count); e != nil {
		return identity.User{}, e
	}
	if quota > 0 && count >= quota {
		return identity.User{}, ErrQuotaExceeded
	}
	u, e := s.identities.CreateUserTx(ctx, tx, identity.CreateUserInput{Username: in.Username, DisplayName: in.DisplayName, Email: email, Password: in.Password})
	if e != nil {
		return identity.User{}, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO tenant_memberships(tenant_id,user_id,created_at) VALUES(?,?,?)`, tenantID, u.ID, stamp(s.now())); e != nil {
		return identity.User{}, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE tenant_invitations SET consumed_at=? WHERE id=?`, stamp(s.now()), in.InvitationID); e != nil {
		return identity.User{}, e
	}
	if e = tx.Commit(); e != nil {
		return identity.User{}, e
	}
	return u, nil
}
func parse(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
