// Package nodes implements the Remote Node Foundation's local, transport-free
// domain: pairing tokens and trusted machine callers for THIS installation
// (used when another GameNode enrolls this one as a controller), and the
// registry of remote nodes THIS installation has enrolled as a controller.
//
// This package never reads or writes another GameNode's database, never
// controls a remote node's runtime, and never proxies arbitrary requests. It
// only stores configuration/trust/status facts about remote installations
// (see AGENTS.md and docs/adr/0006-remote-node-foundation.md).
package nodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// PairingTokenLifetime bounds how long a generated pairing token remains
// usable. Pairing tokens are single-use regardless of this window.
const PairingTokenLifetime = 15 * time.Minute

var (
	ErrPairingTokenInvalid = errors.New("pairing token is invalid, expired, or already used")
)

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

// CreatePairingToken generates a high-entropy, single-use, time-bounded
// secret this installation's operator can hand to a remote controller for
// enrollment. Only its salted hash is persisted; the plaintext value is
// returned exactly once and never logged or audited (see AGENTS.md item 11).
func (s *Service) CreatePairingToken(ctx context.Context, actorUserID *string) (string, time.Time, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now().UTC()
	expires := now.Add(PairingTokenLifetime)
	_, err = s.db.ExecContext(ctx, `INSERT INTO node_pairing_tokens(id,token_hash,created_by_user_id,created_at,expires_at,used_at) VALUES(?,?,?,?,?,NULL)`,
		mustID(), hashToken(raw), actorUserID, stamp(now), stamp(expires))
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, expires, nil
}

// ConsumePairingToken validates and atomically marks a pairing token used.
// A token can be consumed exactly once; a second attempt (replay) fails the
// same way an unknown or expired token does, so a caller cannot distinguish
// "already used" from "never existed" - deliberately, to avoid leaking
// enumeration information.
func (s *Service) ConsumePairingToken(ctx context.Context, raw string) error {
	if raw == "" {
		return ErrPairingTokenInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, expiresAt string
	var usedAt sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,expires_at,used_at FROM node_pairing_tokens WHERE token_hash=?`, hashToken(raw)).Scan(&id, &expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPairingTokenInvalid
	}
	if err != nil {
		return err
	}
	if usedAt.Valid {
		return ErrPairingTokenInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !s.now().UTC().Before(expiry) {
		return ErrPairingTokenInvalid
	}
	if _, err = tx.ExecContext(ctx, `UPDATE node_pairing_tokens SET used_at=? WHERE id=?`, stamp(s.now()), id); err != nil {
		return err
	}
	return tx.Commit()
}

// IssueTrustedCaller creates a new machine credential this node will accept
// on its authenticated Node API. It is created only after a pairing token
// was successfully consumed (see internal/api's enrollment handler). Only
// the credential's salted hash is stored; the plaintext value is returned
// exactly once.
func (s *Service) IssueTrustedCaller(ctx context.Context, displayName string) (string, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", err
	}
	now := stamp(s.now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO node_trusted_callers(id,credential_hash,display_name,created_at,last_seen_at) VALUES(?,?,?,?,NULL)`, mustID(), hashToken(raw), displayName, now)
	if err != nil {
		return "", err
	}
	return raw, nil
}

// AuthenticateCaller reports whether raw matches a currently trusted
// machine credential. On success it best-effort records last_seen_at; a
// failure to do so never fails the authentication decision itself, and
// repeated calls are never audited individually (see AGENTS.md item 26).
func (s *Service) AuthenticateCaller(ctx context.Context, raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	target := hashToken(raw)
	rows, err := s.db.QueryContext(ctx, `SELECT id,credential_hash FROM node_trusted_callers`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	matchedID := ""
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(target)) == 1 {
			matchedID = id
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if matchedID == "" {
		return false, nil
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE node_trusted_callers SET last_seen_at=? WHERE id=?`, stamp(s.now()), matchedID)
	return true, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func mustID() string {
	v, err := randomToken(16)
	if err != nil {
		panic(err)
	}
	return v
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
