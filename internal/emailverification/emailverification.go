// Package emailverification implements the transport-independent, one-time
// email proof used by a future self-registration workflow. It deliberately
// exposes no HTTP registration surface itself.
package emailverification

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
	"time"

	"gamenode/internal/identity"
)

var (
	ErrDisabled    = errors.New("registration email verification is disabled")
	ErrCooldown    = errors.New("a verification email was sent recently")
	ErrInvalid     = errors.New("email verification is invalid")
	ErrExpired     = errors.New("email verification has expired")
	ErrAttempts    = errors.New("email verification attempt limit reached")
	ErrConsumed    = errors.New("email verification has already been consumed")
	ErrPersistence = errors.New("email verification could not be persisted")
)

type Configuration struct {
	Enabled               bool `json:"enabled"`
	LifetimeMinutes       int  `json:"lifetime_minutes"`
	ResendCooldownSeconds int  `json:"resend_cooldown_seconds"`
	MaxAttempts           int  `json:"max_attempts"`
}

type ConfigurationPatch struct {
	Enabled               *bool `json:"enabled,omitempty"`
	LifetimeMinutes       *int  `json:"lifetime_minutes,omitempty"`
	ResendCooldownSeconds *int  `json:"resend_cooldown_seconds,omitempty"`
	MaxAttempts           *int  `json:"max_attempts,omitempty"`
}

type Delivery struct {
	ExpiresAt time.Time `json:"expires_at"`
	RetryAt   time.Time `json:"retry_at"`
}

// Proof contains the opaque identifier returned after the emailed token was
// checked. A future registration transaction must consume this exact proof
// together with the same normalized email address.
type Proof struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	VerifiedAt time.Time `json:"verified_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Sender interface {
	SendEmailVerification(context.Context, string, string, time.Duration) error
}

type Options struct {
	Now func() time.Time
}

type Service struct {
	db      *sql.DB
	sender  Sender
	now     func() time.Time
	issueMu sync.Mutex
}

func New(db *sql.DB, sender Sender, options ...Options) *Service {
	now := func() time.Time { return time.Now().UTC() }
	if len(options) > 0 && options[0].Now != nil {
		now = options[0].Now
	}
	return &Service{db: db, sender: sender, now: now}
}

func (s *Service) GetConfiguration(ctx context.Context) (Configuration, error) {
	var c Configuration
	err := s.db.QueryRowContext(ctx, `SELECT enabled,lifetime_minutes,resend_cooldown_seconds,max_attempts FROM registration_email_verification_settings WHERE singleton=1`).Scan(&c.Enabled, &c.LifetimeMinutes, &c.ResendCooldownSeconds, &c.MaxAttempts)
	if err != nil {
		return Configuration{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	if err = validateConfiguration(c); err != nil {
		return Configuration{}, fmt.Errorf("%w: invalid persisted configuration", ErrPersistence)
	}
	return c, nil
}

func (s *Service) UpdateConfiguration(ctx context.Context, patch ConfigurationPatch) (Configuration, []string, error) {
	c, err := s.GetConfiguration(ctx)
	if err != nil {
		return Configuration{}, nil, err
	}
	changed := []string{}
	applyBool := func(dst *bool, src *bool, name string) {
		if src != nil && *dst != *src {
			*dst = *src
			changed = append(changed, name)
		}
	}
	applyInt := func(dst *int, src *int, name string) {
		if src != nil && *dst != *src {
			*dst = *src
			changed = append(changed, name)
		}
	}
	applyBool(&c.Enabled, patch.Enabled, "registration.email_verification.enabled")
	applyInt(&c.LifetimeMinutes, patch.LifetimeMinutes, "registration.email_verification.lifetime_minutes")
	applyInt(&c.ResendCooldownSeconds, patch.ResendCooldownSeconds, "registration.email_verification.resend_cooldown_seconds")
	applyInt(&c.MaxAttempts, patch.MaxAttempts, "registration.email_verification.max_attempts")
	if err = validateConfiguration(c); err != nil {
		return Configuration{}, nil, err
	}
	if len(changed) == 0 {
		return c, nil, nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE registration_email_verification_settings SET enabled=?,lifetime_minutes=?,resend_cooldown_seconds=?,max_attempts=?,updated_at=? WHERE singleton=1`, c.Enabled, c.LifetimeMinutes, c.ResendCooldownSeconds, c.MaxAttempts, stamp(s.now()))
	if err != nil {
		return Configuration{}, nil, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	return c, changed, nil
}

func validateConfiguration(c Configuration) error {
	if c.LifetimeMinutes < 5 || c.LifetimeMinutes > 1440 {
		return errors.New("verification lifetime must be between 5 and 1440 minutes")
	}
	if c.ResendCooldownSeconds < 30 || c.ResendCooldownSeconds > 3600 {
		return errors.New("verification resend cooldown must be between 30 and 3600 seconds")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 20 {
		return errors.New("verification max attempts must be between 1 and 20")
	}
	return nil
}

// Issue creates and sends one verification token. It returns no token to the
// caller. The process-local lock closes same-node resend races; a later public
// registration endpoint must additionally apply request/IP rate limits.
func (s *Service) Issue(ctx context.Context, rawEmail string) (Delivery, error) {
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		return Delivery{}, err
	}
	if s.sender == nil {
		return Delivery{}, errors.New("email delivery is unavailable")
	}
	s.issueMu.Lock()
	defer s.issueMu.Unlock()
	c, err := s.GetConfiguration(ctx)
	if err != nil {
		return Delivery{}, err
	}
	if !c.Enabled {
		return Delivery{}, ErrDisabled
	}
	now := s.now()
	var latestRaw string
	err = s.db.QueryRowContext(ctx, `SELECT created_at FROM registration_email_verifications WHERE email=? ORDER BY created_at DESC LIMIT 1`, email).Scan(&latestRaw)
	if err == nil {
		latest, parseErr := time.Parse(time.RFC3339Nano, latestRaw)
		if parseErr != nil {
			return Delivery{}, fmt.Errorf("%w: invalid challenge timestamp", ErrPersistence)
		}
		if now.Before(latest.Add(time.Duration(c.ResendCooldownSeconds) * time.Second)) {
			return Delivery{}, ErrCooldown
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	id, token, hash, err := newChallenge()
	if err != nil {
		return Delivery{}, err
	}
	expires := now.Add(time.Duration(c.LifetimeMinutes) * time.Minute)
	_, err = s.db.ExecContext(ctx, `INSERT INTO registration_email_verifications(id,email,token_hash,expires_at,created_at) VALUES(?,?,?,?,?)`, id, email, hash, stamp(expires), stamp(now))
	if err != nil {
		return Delivery{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	if err = s.sender.SendEmailVerification(ctx, email, token, time.Duration(c.LifetimeMinutes)*time.Minute); err != nil {
		_, _ = s.db.ExecContext(context.Background(), `DELETE FROM registration_email_verifications WHERE id=? AND verified_at IS NULL AND consumed_at IS NULL`, id)
		return Delivery{}, err
	}
	// Only a successfully delivered new token supersedes older proofs.
	if _, err = s.db.ExecContext(ctx, `UPDATE registration_email_verifications SET consumed_at=? WHERE email=? AND id<>? AND consumed_at IS NULL`, stamp(now), email, id); err != nil {
		return Delivery{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	// Keep persistent challenge storage bounded without placing an unbounded
	// cleanup operation on the registration path.
	_, _ = s.db.ExecContext(context.Background(), `DELETE FROM registration_email_verifications WHERE id IN (SELECT id FROM registration_email_verifications WHERE expires_at<? LIMIT 100)`, stamp(now.Add(-24*time.Hour)))
	return Delivery{ExpiresAt: expires, RetryAt: now.Add(time.Duration(c.ResendCooldownSeconds) * time.Second)}, nil
}

// Verify checks the emailed token in constant time and returns an opaque proof
// for the future registration flow. Wrong attempts are persisted and bounded.
func (s *Service) Verify(ctx context.Context, rawEmail, token string) (Proof, error) {
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		return Proof{}, ErrInvalid
	}
	if len(token) < 32 || len(token) > 128 || strings.ContainsAny(token, "\r\n\x00") {
		return Proof{}, ErrInvalid
	}
	c, err := s.GetConfiguration(ctx)
	if err != nil {
		return Proof{}, err
	}
	if !c.Enabled {
		return Proof{}, ErrDisabled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Proof{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	defer tx.Rollback()
	var id, expiresRaw string
	var storedHash []byte
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT id,token_hash,attempts,expires_at FROM registration_email_verifications WHERE email=? AND consumed_at IS NULL ORDER BY created_at DESC LIMIT 1`, email).Scan(&id, &storedHash, &attempts, &expiresRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Proof{}, ErrInvalid
	}
	if err != nil {
		return Proof{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresRaw)
	if err != nil {
		return Proof{}, fmt.Errorf("%w: invalid challenge expiry", ErrPersistence)
	}
	now := s.now()
	if !now.Before(expires) {
		_, _ = tx.ExecContext(ctx, `UPDATE registration_email_verifications SET consumed_at=? WHERE id=?`, stamp(now), id)
		if commitErr := tx.Commit(); commitErr != nil {
			return Proof{}, fmt.Errorf("%w: %v", ErrPersistence, commitErr)
		}
		return Proof{}, ErrExpired
	}
	if attempts >= c.MaxAttempts {
		return Proof{}, ErrAttempts
	}
	hash := sha256.Sum256([]byte(token))
	if len(storedHash) != len(hash) || subtle.ConstantTimeCompare(storedHash, hash[:]) != 1 {
		attempts++
		consume := any(nil)
		if attempts >= c.MaxAttempts {
			consume = stamp(now)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE registration_email_verifications SET attempts=?,consumed_at=COALESCE(?,consumed_at) WHERE id=?`, attempts, consume, id); err != nil {
			return Proof{}, fmt.Errorf("%w: %v", ErrPersistence, err)
		}
		if err = tx.Commit(); err != nil {
			return Proof{}, fmt.Errorf("%w: %v", ErrPersistence, err)
		}
		if attempts >= c.MaxAttempts {
			return Proof{}, ErrAttempts
		}
		return Proof{}, ErrInvalid
	}
	verified := now
	if _, err = tx.ExecContext(ctx, `UPDATE registration_email_verifications SET verified_at=COALESCE(verified_at,?) WHERE id=?`, stamp(verified), id); err != nil {
		return Proof{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	if err = tx.Commit(); err != nil {
		return Proof{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	return Proof{ID: id, Email: email, VerifiedAt: verified, ExpiresAt: expires}, nil
}

// ConsumeProofTx must be called inside the future user-creation transaction.
// This makes consuming the proof and inserting the user one atomic commit.
func (s *Service) ConsumeProofTx(ctx context.Context, tx *sql.Tx, proofID, rawEmail string) error {
	if tx == nil {
		return errors.New("transaction is required")
	}
	email, err := normalizeEmail(rawEmail)
	if err != nil || len(proofID) != 36 {
		return ErrInvalid
	}
	var storedEmail, expiresRaw string
	var consumed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT email,expires_at,consumed_at FROM registration_email_verifications WHERE id=? AND verified_at IS NOT NULL`, proofID).Scan(&storedEmail, &expiresRaw, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	if !strings.EqualFold(storedEmail, email) {
		return ErrInvalid
	}
	if consumed.Valid {
		return ErrConsumed
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresRaw)
	if err != nil {
		return fmt.Errorf("%w: invalid proof expiry", ErrPersistence)
	}
	if !s.now().Before(expires) {
		return ErrExpired
	}
	result, err := tx.ExecContext(ctx, `UPDATE registration_email_verifications SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, stamp(s.now()), proofID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	if changed != 1 {
		return ErrConsumed
	}
	return nil
}

func normalizeEmail(value string) (string, error) {
	value, err := identity.NormalizeEmail(value)
	if err != nil {
		return "", err
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return "", errors.New("a valid email is required")
	}
	return strings.ToLower(value), nil
}

func newChallenge() (string, string, []byte, error) {
	idRaw := make([]byte, 16)
	tokenRaw := make([]byte, 32)
	if _, err := rand.Read(idRaw); err != nil {
		return "", "", nil, err
	}
	if _, err := rand.Read(tokenRaw); err != nil {
		return "", "", nil, err
	}
	id := hex.EncodeToString(idRaw[0:4]) + "-" + hex.EncodeToString(idRaw[4:6]) + "-4" + hex.EncodeToString(idRaw[6:8])[1:] + "-a" + hex.EncodeToString(idRaw[8:10])[1:] + "-" + hex.EncodeToString(idRaw[10:])
	token := base64.RawURLEncoding.EncodeToString(tokenRaw)
	hash := sha256.Sum256([]byte(token))
	return id, token, hash[:], nil
}

func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
