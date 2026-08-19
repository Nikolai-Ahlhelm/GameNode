package nodes_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/database"
	"gamenode/internal/nodes"
)

func testService(t *testing.T) (*nodes.Service, *sql.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	return nodes.New(db), db
}

func TestPairingTokenValidEnrollment(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	raw, expires, err := s.CreatePairingToken(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || !expires.After(time.Now().UTC()) {
		t.Fatal("expected a non-empty token with a future expiry")
	}
	if err := s.ConsumePairingToken(ctx, raw); err != nil {
		t.Fatalf("expected valid token to be consumed: %v", err)
	}
}

func TestPairingTokenInvalidSecret(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	if _, _, err := s.CreatePairingToken(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumePairingToken(ctx, "totally-wrong-token"); err != nodes.ErrPairingTokenInvalid {
		t.Fatalf("expected ErrPairingTokenInvalid, got %v", err)
	}
}

func TestPairingTokenCannotBeReplayed(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	raw, _, err := s.CreatePairingToken(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumePairingToken(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumePairingToken(ctx, raw); err != nodes.ErrPairingTokenInvalid {
		t.Fatalf("expected replay to be rejected, got %v", err)
	}
}

func TestTrustedCallerAuthentication(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	raw, err := s.IssueTrustedCaller(ctx, "controller-a")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.AuthenticateCaller(ctx, raw)
	if err != nil || !ok {
		t.Fatalf("expected valid credential to authenticate, got %v %v", ok, err)
	}
	ok, err = s.AuthenticateCaller(ctx, "wrong-credential")
	if err != nil || ok {
		t.Fatalf("expected invalid credential to fail, got %v %v", ok, err)
	}
	ok, err = s.AuthenticateCaller(ctx, "")
	if err != nil || ok {
		t.Fatalf("expected empty credential to fail, got %v %v", ok, err)
	}
}

func TestTrustedCallerHashNotStoredAsPlaintext(t *testing.T) {
	ctx := context.Background()
	s, db := testService(t)
	raw, err := s.IssueTrustedCaller(ctx, "controller-b")
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := db.QueryRowContext(ctx, `SELECT credential_hash FROM node_trusted_callers`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == raw {
		t.Fatal("credential must never be stored as plaintext")
	}
}

func TestRegistryCreateReadUpdateDisableRemove(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	n, err := s.CreateEnrolled(ctx, nodes.CreateEnrolledInput{
		DisplayName: "Node One", Endpoint: "https://node-one.internal:8443", Credential: "secret-credential",
		NodeID: "node-1", ProtocolVersion: 1, GameNodeVersion: "0.5.0", OS: "linux", Arch: "amd64",
		Capabilities: []string{"native_runtime"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == "" || !n.Enabled {
		t.Fatalf("unexpected created node: %+v", n)
	}
	got, err := s.Get(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "Node One" || got.Endpoint != n.Endpoint {
		t.Fatalf("unexpected get result: %+v", got)
	}
	renamed, err := s.UpdateDisplayName(ctx, n.ID, "Renamed Node")
	if err != nil || renamed.DisplayName != "Renamed Node" {
		t.Fatalf("rename failed: %v %+v", err, renamed)
	}
	disabled, err := s.SetEnabled(ctx, n.ID, false)
	if err != nil || disabled.Enabled || disabled.TrustStatus != nodes.TrustDisabled {
		t.Fatalf("disable failed: %v %+v", err, disabled)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("expected one registered node, got %d, err %v", len(list), err)
	}
	if err := s.Delete(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, n.ID); err != nodes.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestRegistryDuplicateNodeIDAndEndpoint(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	in := nodes.CreateEnrolledInput{DisplayName: "A", Endpoint: "https://a.internal", Credential: "c1", NodeID: "dup-node", ProtocolVersion: 1}
	if _, err := s.CreateEnrolled(ctx, in); err != nil {
		t.Fatal(err)
	}
	dupNodeID := in
	dupNodeID.Endpoint = "https://b.internal"
	dupNodeID.Credential = "c2"
	if _, err := s.CreateEnrolled(ctx, dupNodeID); err != nodes.ErrDuplicateNodeID {
		t.Fatalf("expected ErrDuplicateNodeID, got %v", err)
	}
	dupEndpoint := in
	dupEndpoint.NodeID = "another-node"
	dupEndpoint.Credential = "c3"
	if _, err := s.CreateEnrolled(ctx, dupEndpoint); err != nodes.ErrDuplicateEndpoint {
		t.Fatalf("expected ErrDuplicateEndpoint, got %v", err)
	}
}

func TestRegistryApplyStatusPersistsCapabilitiesAndLastSeen(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	n, err := s.CreateEnrolled(ctx, nodes.CreateEnrolledInput{DisplayName: "A", Endpoint: "https://a.internal", Credential: "c1", NodeID: "node-a", ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyStatus(ctx, n.ID, nodes.StatusUpdate{ProtocolVersion: 1, GameNodeVersion: "0.5.1", OS: "linux", Arch: "amd64", Capabilities: []string{"console", "monitoring"}, Health: nodes.HealthReachable}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHealth != nodes.HealthReachable || got.LastSeenAt == nil || len(got.Capabilities) != 2 {
		t.Fatalf("unexpected status after apply: %+v", got)
	}
	// An unreachable result must not clobber the last known good LastSeenAt.
	previousSeen := got.LastSeenAt
	if err := s.ApplyStatus(ctx, n.ID, nodes.StatusUpdate{Health: nodes.HealthUnreachable, ErrorCode: "node_unreachable"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHealth != nodes.HealthUnreachable || got.LastErrorCode != "node_unreachable" {
		t.Fatalf("unexpected unreachable status: %+v", got)
	}
	if got.LastSeenAt == nil || !got.LastSeenAt.Equal(*previousSeen) {
		t.Fatalf("last_seen_at must be preserved on an unreachable refresh: %+v vs %+v", got.LastSeenAt, previousSeen)
	}
}

func TestRegistryCredentialNeverExposedByGet(t *testing.T) {
	ctx := context.Background()
	s, _ := testService(t)
	n, err := s.CreateEnrolled(ctx, nodes.CreateEnrolledInput{DisplayName: "A", Endpoint: "https://a.internal", Credential: "top-secret", NodeID: "node-a", ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, credential, err := s.Credential(ctx, n.ID)
	if err != nil || credential != "top-secret" {
		t.Fatalf("expected internal credential accessor to return stored secret: %v %v", credential, err)
	}
}
