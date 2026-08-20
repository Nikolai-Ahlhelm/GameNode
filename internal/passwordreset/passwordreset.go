package passwordreset

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"gamenode/internal/identity"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidToken    = errors.New("invalid password reset token")
	ErrNotFound        = errors.New("password reset token not found")
	ErrExpired         = errors.New("password reset token expired")
	ErrConsumed        = errors.New("password reset token already used")
	ErrTooManyAttempts = errors.New("too many password reset attempts")
)

type Sender interface {
	SendPasswordReset(context.Context, string, string, time.Duration) error
}
type Service struct {
	db     *sql.DB
	ids    *identity.Service
	sender Sender
	now    func() time.Time
}

const lifetime = 1 * time.Hour

type RequestResult struct {
	Accepted bool `json:"accepted"`
}

func New(db *sql.DB, ids *identity.Service, sender Sender) *Service {
	return &Service{db: db, ids: ids, sender: sender, now: time.Now}
}
func email(v string) (string, error) {
	v = strings.TrimSpace(v)
	a, e := mail.ParseAddress(v)
	if e != nil || a.Address != v {
		return "", errors.New("invalid email")
	}
	return strings.ToLower(v), nil
}
func token() (string, []byte, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", nil, e
	}
	t := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(t))
	return t, h[:], nil
}
func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Service) Request(ctx context.Context, address, linkBase string) (RequestResult, error) {
	eaddr, e := email(address)
	if e != nil {
		return RequestResult{Accepted: true}, nil
	}
	var userID string
	var enabled int
	e = s.db.QueryRowContext(ctx, `SELECT id,disabled FROM users WHERE email=? COLLATE NOCASE`, eaddr).Scan(&userID, &enabled)
	if errors.Is(e, sql.ErrNoRows) || enabled != 0 {
		return RequestResult{Accepted: true}, nil
	}
	if e != nil {
		return RequestResult{}, e
	}
	t, h, e := token()
	if e != nil {
		return RequestResult{}, e
	}
	now := s.now().UTC()
	rid := id()
	_, e = s.db.ExecContext(ctx, `INSERT INTO password_reset_tokens(id,user_id,token_hash,expires_at,created_at) VALUES(?,?,?,?,?)`, rid, userID, h, now.Add(lifetime).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if e != nil {
		return RequestResult{}, e
	}
	link := strings.TrimRight(linkBase, "/") + "/reset-password?reset_id=" + url.QueryEscape(rid) + "&token=" + url.QueryEscape(t)
	if s.sender == nil || s.sender.SendPasswordReset(ctx, eaddr, link, lifetime) != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM password_reset_tokens WHERE id=?`, rid)
	}
	return RequestResult{Accepted: true}, nil
}
func (s *Service) Consume(ctx context.Context, rid, tok, password string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var uid string
	var h []byte
	var exp string
	var used sql.NullString
	var attempts int
	e = tx.QueryRowContext(ctx, `SELECT user_id,token_hash,expires_at,consumed_at,attempts FROM password_reset_tokens WHERE id=?`, rid).Scan(&uid, &h, &exp, &used, &attempts)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if used.Valid {
		return ErrConsumed
	}
	if attempts >= 5 {
		return ErrTooManyAttempts
	}
	parsed, _ := time.Parse(time.RFC3339Nano, exp)
	if !parsed.After(s.now()) {
		return ErrExpired
	}
	sum := sha256.Sum256([]byte(tok))
	if len(h) != len(sum) || subtle.ConstantTimeCompare(h, sum[:]) != 1 {
		_, _ = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET attempts=attempts+1 WHERE id=?`, rid)
		_ = tx.Commit()
		return ErrInvalidToken
	}
	if e = s.ids.ResetPasswordTx(ctx, tx, uid, password); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE password_reset_tokens SET consumed_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), rid); e != nil {
		return e
	}
	return tx.Commit()
}
