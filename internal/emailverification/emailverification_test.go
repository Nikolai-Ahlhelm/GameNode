package emailverification

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/database"
)

type fakeSender struct {
	email, token string
	lifetime     time.Duration
	err          error
}

func (f *fakeSender) SendEmailVerification(_ context.Context, email, token string, lifetime time.Duration) error {
	f.email, f.token, f.lifetime = email, token, lifetime
	return f.err
}

func testService(t *testing.T) (*Service, *fakeSender, *sql.DB, *time.Time) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	sender := &fakeSender{}
	service := New(db, sender, Options{Now: func() time.Time { return now }})
	t.Cleanup(func() { db.Close() })
	return service, sender, db, &now
}

func enable(t *testing.T, service *Service, maxAttempts int) {
	t.Helper()
	enabled := true
	if _, _, err := service.UpdateConfiguration(context.Background(), ConfigurationPatch{Enabled: &enabled, MaxAttempts: &maxAttempts}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueVerifyAndAtomicallyConsumeProof(t *testing.T) {
	service, sender, db, _ := testService(t)
	enable(t, service, 5)
	delivery, err := service.Issue(context.Background(), " User@Example.Test ")
	if err != nil {
		t.Fatal(err)
	}
	if sender.email != "user@example.test" || len(sender.token) < 32 || sender.lifetime != 30*time.Minute {
		t.Fatalf("unexpected delivery: %#v", sender)
	}
	if !delivery.ExpiresAt.Equal(time.Date(2026, 8, 20, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("unexpected expiry: %v", delivery.ExpiresAt)
	}
	var stored []byte
	if err = db.QueryRow(`SELECT token_hash FROM registration_email_verifications WHERE email=?`, sender.email).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == sender.token {
		t.Fatal("raw verification token was persisted")
	}
	proof, err := service.Verify(context.Background(), sender.email, sender.token)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Email != sender.email || len(proof.ID) != 36 {
		t.Fatalf("unexpected proof: %+v", proof)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ConsumeProofTx(context.Background(), tx, proof.ID, sender.email); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.ConsumeProofTx(context.Background(), tx, proof.ID, sender.email); !errors.Is(err, ErrConsumed) {
		tx.Rollback()
		t.Fatalf("proof replay result: %v", err)
	}
	tx.Rollback()
}

func TestWrongTokensAreAttemptLimited(t *testing.T) {
	service, sender, _, _ := testService(t)
	enable(t, service, 2)
	if _, err := service.Issue(context.Background(), "user@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(context.Background(), sender.email, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("first attempt: %v", err)
	}
	if _, err := service.Verify(context.Background(), sender.email, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); !errors.Is(err, ErrAttempts) {
		t.Fatalf("second attempt: %v", err)
	}
	if _, err := service.Verify(context.Background(), sender.email, sender.token); !errors.Is(err, ErrInvalid) {
		t.Fatalf("consumed challenge should be invalid: %v", err)
	}
}

func TestExpiryCooldownAndFailedDelivery(t *testing.T) {
	service, sender, db, now := testService(t)
	enable(t, service, 5)
	if _, err := service.Issue(context.Background(), "user@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), "user@example.test"); !errors.Is(err, ErrCooldown) {
		t.Fatalf("cooldown result: %v", err)
	}
	*now = now.Add(31 * time.Minute)
	if _, err := service.Verify(context.Background(), sender.email, sender.token); !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry result: %v", err)
	}
	*now = now.Add(time.Minute)
	sender.err = errors.New("SMTP unavailable")
	if _, err := service.Issue(context.Background(), "other@example.test"); err == nil {
		t.Fatal("delivery failure accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM registration_email_verifications WHERE email='other@example.test'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed delivery left challenge: count=%d err=%v", count, err)
	}
}

func TestConfigurationIsBoundedAndDisabledByDefault(t *testing.T) {
	service, _, _, _ := testService(t)
	c, err := service.GetConfiguration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Enabled || c.LifetimeMinutes != 30 || c.ResendCooldownSeconds != 60 || c.MaxAttempts != 5 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	invalid := 1
	if _, _, err = service.UpdateConfiguration(context.Background(), ConfigurationPatch{LifetimeMinutes: &invalid}); err == nil {
		t.Fatal("invalid lifetime accepted")
	}
	if _, err = service.Issue(context.Background(), "user@example.test"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled result: %v", err)
	}
}
