package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const sessionLifetime = 24 * time.Hour

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type User struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	IsAdmin     bool   `json:"is_admin"`
}
type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

// Database is exposed to the local identity service, which shares the users
// and sessions schema but owns administration workflows.
func (s *Service) Database() *sql.DB { return s.db }

func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_admin=1").Scan(&n)
	return n == 0, err
}
func validate(username, email, password string) error {
	if _, err := NormalizeUsername(username); err != nil {
		return err
	}
	if !strings.Contains(email, "@") || len(email) > 254 {
		return errors.New("a valid email is required")
	}
	if len(password) < 12 || len(password) > 256 {
		return errors.New("password must be 12 to 256 characters")
	}
	return nil
}

// NormalizeUsername accepts a deliberately ASCII-only identifier. SQLite's
// NOCASE collation gives reliable case-insensitive uniqueness for this set and
// avoiding Unicode identifiers prevents invisible normalization collisions.
func NormalizeUsername(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || len(value) > 32 || !usernamePattern.MatchString(value) {
		return "", errors.New("username must be 3 to 32 ASCII letters, digits, dots, hyphens, or underscores")
	}
	return value, nil
}
func (s *Service) CreateInitialAdmin(ctx context.Context, username, email, password string) (User, error) {
	if err := validate(username, email, password); err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE is_admin=1").Scan(&n); err != nil {
		return User{}, err
	}
	if n != 0 {
		return User{}, errors.New("initial setup has already been completed")
	}
	id, err := token(16)
	if err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	username, _ = NormalizeUsername(username)
	_, err = tx.ExecContext(ctx, "INSERT INTO users(id,username,email,password_hash,is_admin,created_at,updated_at) VALUES(?,?,?,?,1,?,?)", id, username, strings.TrimSpace(email), hash, now, now)
	if err != nil {
		return User{}, fmt.Errorf("create administrator: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, IsAdmin: true}, nil
}
func HashPassword(password string) (string, error) {
	salt, err := random(16)
	if err != nil {
		return "", err
	}
	p := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return "$argon2id$v=19$m=65536,t=3,p=4$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(p), nil
}
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[4])
	wanted, e2 := base64.RawStdEncoding.DecodeString(parts[5])
	if e1 != nil || e2 != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, uint32(len(wanted)))
	return subtleEqual(actual, wanted)
}
func subtleEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var d byte
	for i := range a {
		d |= a[i] ^ b[i]
	}
	return d == 0
}
func (s *Service) Login(ctx context.Context, username, password string) (User, string, string, error) {
	var u User
	var hash string
	var disabled int
	err := s.db.QueryRowContext(ctx, "SELECT id,username,display_name,password_hash,is_admin,disabled FROM users WHERE username=?", strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.DisplayName, &hash, &u.IsAdmin, &disabled)
	if err != nil || disabled != 0 || !VerifyPassword(hash, password) {
		return User{}, "", "", errors.New("invalid credentials")
	}
	_, _ = s.db.ExecContext(ctx, "UPDATE users SET last_login_at=? WHERE id=?", s.now().UTC().Format(time.RFC3339Nano), u.ID)
	raw, csrf, e := s.CreateSession(ctx, u)
	return u, raw, csrf, e
}
func (s *Service) CreateSession(ctx context.Context, u User) (string, string, error) {
	raw, e := token(32)
	if e != nil {
		return "", "", e
	}
	csrf, e := token(24)
	if e != nil {
		return "", "", e
	}
	now := s.now().UTC()
	_, e = s.db.ExecContext(ctx, "INSERT INTO sessions(id,user_id,token_hash,csrf_token,expires_at,created_at,last_seen_at) VALUES(?,?,?,?,?,?,?)", mustToken(16), u.ID, digest(raw), csrf, now.Add(sessionLifetime).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return raw, csrf, e
}
func (s *Service) Current(ctx context.Context, raw string) (User, string, error) {
	if raw == "" {
		return User{}, "", errors.New("unauthenticated")
	}
	var u User
	var csrf, expires string
	err := s.db.QueryRowContext(ctx, "SELECT u.id,u.username,u.display_name,u.is_admin,s.csrf_token,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND u.disabled=0", digest(raw)).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &csrf, &expires)
	if err != nil {
		return User{}, "", errors.New("unauthenticated")
	}
	expiry, e := time.Parse(time.RFC3339Nano, expires)
	if e != nil || !expiry.After(s.now()) {
		return User{}, "", errors.New("unauthenticated")
	}
	return u, csrf, nil
}
func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", digest(raw))
	return err
}
func digest(raw string) []byte     { sum := sha256.Sum256([]byte(raw)); return sum[:] }
func random(n int) ([]byte, error) { b := make([]byte, n); _, e := rand.Read(b); return b, e }
func token(n int) (string, error) {
	b, e := random(n)
	return base64.RawURLEncoding.EncodeToString(b), e
}
func mustToken(n int) string {
	v, e := token(n)
	if e != nil {
		panic(e)
	}
	return v
}
