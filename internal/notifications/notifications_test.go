package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/database"
)

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	s := New(db, slog.Default())
	t.Cleanup(func() { s.Close(); db.Close() })
	return s
}

func TestConfigurationPersistsWithoutExposingPassword(t *testing.T) {
	s := testService(t)
	enabled := true
	host, security, username, password := "smtp.example.test", "starttls", "mailer", "secret"
	from, prefix := "gamenode@example.test", "[Ops]"
	port := 587
	recipients := []string{"admin@example.test", "ops@example.test"}
	crashed := true
	value, changed, err := s.Update(context.Background(), Patch{Enabled: &enabled, SMTPHost: &host, SMTPPort: &port, SMTPSecurity: &security, SMTPUsername: &username, SMTPPassword: &password, FromAddress: &from, Recipients: &recipients, SubjectPrefix: &prefix, Events: &EventsPatch{Crashed: &crashed}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) == 0 || !value.PasswordConfigured || value.SMTPHost != host {
		t.Fatalf("unexpected configuration: %+v %#v", value, changed)
	}
	encoded, _ := json.Marshal(value)
	if string(encoded) == "" || contains(string(encoded), `"`+password+`"`) {
		t.Fatalf("password leaked in JSON: %s", encoded)
	}
	reloaded, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.PasswordConfigured || reloaded.password != password {
		t.Fatal("password was not persisted internally")
	}
}

func TestAuthenticationRequiresEncryptedTransport(t *testing.T) {
	s := testService(t)
	security, username := "none", "mailer"
	if _, _, err := s.Update(context.Background(), Patch{SMTPSecurity: &security, SMTPUsername: &username}); err == nil {
		t.Fatal("unencrypted SMTP authentication accepted")
	}
}

func TestEnabledEventIsDeliveredFromBoundedQueue(t *testing.T) {
	s := testService(t)
	enabled := true
	host := "smtp.example.test"
	from := "gamenode@example.test"
	recipients := []string{"admin@example.test"}
	if _, _, err := s.Update(context.Background(), Patch{Enabled: &enabled, SMTPHost: &host, FromAddress: &from, Recipients: &recipients}); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan string, 1)
	s.sender = func(_ context.Context, _ Configuration, subject, _ string) error { delivered <- subject; return nil }
	s.Enqueue(Event{Type: "crashed", ServerID: "server-1", ServerName: "Valheim", TenantID: "default", OccurredAt: time.Now()})
	select {
	case subject := <-delivered:
		if subject != "[GameNode] Valheim — crashed" {
			t.Fatalf("unexpected subject %q", subject)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not delivered")
	}
}

func TestVerificationMailUsesConfiguredSMTPWithoutAlertRecipients(t *testing.T) {
	s := testService(t)
	host, from := "smtp.example.test", "gamenode@example.test"
	if _, _, err := s.Update(context.Background(), Patch{SMTPHost: &host, FromAddress: &from}); err != nil {
		t.Fatal(err)
	}
	var got Configuration
	var subjectLine, body string
	s.sender = func(_ context.Context, c Configuration, subject, message string) error {
		got, subjectLine, body = c, subject, message
		return nil
	}
	token := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	if err := s.SendEmailVerification(context.Background(), "user@example.test", token, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(got.Recipients) != 1 || got.Recipients[0] != "user@example.test" || subjectLine != "[GameNode] Verify your email address" || !contains(body, token) {
		t.Fatalf("unexpected verification email: recipients=%v subject=%q body=%q", got.Recipients, subjectLine, body)
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
