package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound           = errors.New("remote node not found")
	ErrDuplicateNodeID    = errors.New("a remote node with this node id is already enrolled")
	ErrDuplicateEndpoint  = errors.New("a remote node with this endpoint is already enrolled")
	ErrEndpointInvalid    = errors.New("remote node endpoint is invalid")
	ErrDisplayNameInvalid = errors.New("display name is required")
)

// Health is this installation's best-effort, presentation-only view of a
// remote node's reachability. It is never treated as authoritative over the
// remote node's own server/runtime lifecycle (see AGENTS.md item 21/35).
type Health string

const (
	HealthUnknown              Health = "unknown"
	HealthReachable            Health = "reachable"
	HealthUnreachable          Health = "unreachable"
	HealthAuthenticationFailed Health = "authentication_failed"
	HealthProtocolIncompatible Health = "protocol_incompatible"
	HealthDegraded             Health = "degraded"
)

// TrustStatus describes this registry entry's enrollment lifecycle.
type TrustStatus string

const (
	TrustEnrolled TrustStatus = "enrolled"
	TrustDisabled TrustStatus = "disabled"
)

// RemoteNode is configuration and last-known status for another GameNode
// installation this one manages as a controller. Credential is deliberately
// unexported from the JSON view produced by the API layer; see
// internal/api/remotenodes.go's public projection.
type RemoteNode struct {
	ID              string
	NodeID          string
	DisplayName     string
	Endpoint        string
	Credential      string
	ProtocolVersion int
	GameNodeVersion string
	OS              string
	Arch            string
	Capabilities    []string
	Enabled         bool
	TrustStatus     TrustStatus
	LastSeenAt      *time.Time
	LastHealth      Health
	LastErrorCode   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type CreateEnrolledInput struct {
	DisplayName     string
	Endpoint        string
	Credential      string
	NodeID          string
	ProtocolVersion int
	GameNodeVersion string
	OS              string
	Arch            string
	Capabilities    []string
}

// CreateEnrolled persists a remote node registry entry after a successful
// enrollment handshake (see internal/remote.Client.Enroll and
// internal/api/remotenodes.go). It never accepts a bare endpoint without a
// credential obtained through that handshake - there is no "trust this URL"
// path in this package.
func (s *Service) CreateEnrolled(ctx context.Context, in CreateEnrolledInput) (RemoteNode, error) {
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = in.NodeID
	}
	if displayName == "" {
		return RemoteNode{}, ErrDisplayNameInvalid
	}
	if strings.TrimSpace(in.Endpoint) == "" || strings.TrimSpace(in.Credential) == "" || strings.TrimSpace(in.NodeID) == "" {
		return RemoteNode{}, ErrEndpointInvalid
	}
	now := s.now().UTC()
	capabilities, err := json.Marshal(in.Capabilities)
	if err != nil {
		return RemoteNode{}, err
	}
	n := RemoteNode{
		ID: mustID(), NodeID: in.NodeID, DisplayName: displayName, Endpoint: in.Endpoint, Credential: in.Credential,
		ProtocolVersion: in.ProtocolVersion, GameNodeVersion: in.GameNodeVersion, OS: in.OS, Arch: in.Arch,
		Capabilities: in.Capabilities, Enabled: true, TrustStatus: TrustEnrolled, LastHealth: HealthReachable,
		CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO remote_nodes(id,node_id,display_name,endpoint,credential,protocol_version,gamenode_version,os,arch,capabilities_json,enabled,trust_status,last_seen_at,last_health,last_error_code,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,?)`,
		n.ID, n.NodeID, n.DisplayName, n.Endpoint, n.Credential, n.ProtocolVersion, n.GameNodeVersion, n.OS, n.Arch, string(capabilities), string(TrustEnrolled), stamp(now), string(HealthReachable), "", stamp(now), stamp(now))
	if err = classifyConstraint(err); err != nil {
		return RemoteNode{}, err
	}
	n.LastSeenAt = &now
	return n, nil
}

func (s *Service) List(ctx context.Context) ([]RemoteNode, error) {
	rows, err := s.db.QueryContext(ctx, remoteNodeSelect+" ORDER BY display_name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemoteNode
	for rows.Next() {
		n, err := scanRemoteNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id string) (RemoteNode, error) {
	n, err := scanRemoteNode(s.db.QueryRowContext(ctx, remoteNodeSelect+" WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteNode{}, ErrNotFound
	}
	return n, err
}

// UpdateDisplayName renames the local registry entry only. It never
// contacts or renames the remote node itself.
func (s *Service) UpdateDisplayName(ctx context.Context, id, displayName string) (RemoteNode, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return RemoteNode{}, ErrDisplayNameInvalid
	}
	if _, err := s.Get(ctx, id); err != nil {
		return RemoteNode{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE remote_nodes SET display_name=?,updated_at=? WHERE id=?`, displayName, stamp(s.now()), id); err != nil {
		return RemoteNode{}, err
	}
	return s.Get(ctx, id)
}

// SetEnabled toggles whether this node participates in status refresh and
// remains available for future (v0.5B) operations. Disabling never contacts
// the remote node and never affects its own local workloads (see AGENTS.md
// item 34/35).
func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool) (RemoteNode, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return RemoteNode{}, err
	}
	status := TrustEnrolled
	if !enabled {
		status = TrustDisabled
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE remote_nodes SET enabled=?,trust_status=?,updated_at=? WHERE id=?`, boolToInt(enabled), string(status), stamp(s.now()), id); err != nil {
		return RemoteNode{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM remote_nodes WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// StatusUpdate carries the fields a bounded health refresh (internal/remote)
// is allowed to persist. It never includes anything about the remote node's
// own servers (see AGENTS.md item 21).
type StatusUpdate struct {
	ProtocolVersion int
	GameNodeVersion string
	OS              string
	Arch            string
	Capabilities    []string
	Health          Health
	ErrorCode       string
}

// ApplyStatus persists the result of one health/info refresh. It is safe to
// call frequently; callers are expected NOT to audit every call (see
// AGENTS.md item 26/27) - only user-visible registry mutations are audited.
func (s *Service) ApplyStatus(ctx context.Context, id string, in StatusUpdate) error {
	capabilities, err := json.Marshal(in.Capabilities)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	lastSeen := any(nil)
	if in.Health == HealthReachable {
		lastSeen = stamp(now)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE remote_nodes SET protocol_version=?,gamenode_version=?,os=?,arch=?,capabilities_json=?,last_health=?,last_error_code=?,updated_at=?, last_seen_at=COALESCE(?, last_seen_at) WHERE id=?`,
		in.ProtocolVersion, in.GameNodeVersion, in.OS, in.Arch, string(capabilities), string(in.Health), in.ErrorCode, stamp(now), lastSeen, id)
	return err
}

// Credential returns the machine credential this controller presents to the
// remote node. It is used only by the bounded status refresher and the
// remote client wrapper - never returned through the controller-facing API.
func (s *Service) Credential(ctx context.Context, id string) (string, string, error) {
	var endpoint, credential string
	err := s.db.QueryRowContext(ctx, `SELECT endpoint,credential FROM remote_nodes WHERE id=?`, id).Scan(&endpoint, &credential)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return endpoint, credential, err
}

const remoteNodeSelect = "SELECT id,node_id,display_name,endpoint,credential,protocol_version,gamenode_version,os,arch,capabilities_json,enabled,trust_status,last_seen_at,last_health,last_error_code,created_at,updated_at FROM remote_nodes"

type scanner interface{ Scan(...any) error }

func scanRemoteNode(row scanner) (RemoteNode, error) {
	var n RemoteNode
	var enabled int
	var trustStatus, health, capabilitiesJSON, created, updated string
	var lastSeen sql.NullString
	if err := row.Scan(&n.ID, &n.NodeID, &n.DisplayName, &n.Endpoint, &n.Credential, &n.ProtocolVersion, &n.GameNodeVersion, &n.OS, &n.Arch, &capabilitiesJSON, &enabled, &trustStatus, &lastSeen, &health, &n.LastErrorCode, &created, &updated); err != nil {
		return RemoteNode{}, err
	}
	n.Enabled = enabled != 0
	n.TrustStatus = TrustStatus(trustStatus)
	n.LastHealth = Health(health)
	_ = json.Unmarshal([]byte(capabilitiesJSON), &n.Capabilities)
	var err error
	if n.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return RemoteNode{}, err
	}
	if n.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return RemoteNode{}, err
	}
	if lastSeen.Valid {
		t, err := time.Parse(time.RFC3339Nano, lastSeen.String)
		if err != nil {
			return RemoteNode{}, err
		}
		n.LastSeenAt = &t
	}
	return n, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
	case strings.Contains(message, "remote_nodes.node_id"):
		return ErrDuplicateNodeID
	case strings.Contains(message, "remote_nodes.endpoint"):
		return ErrDuplicateEndpoint
	default:
		return err
	}
}
