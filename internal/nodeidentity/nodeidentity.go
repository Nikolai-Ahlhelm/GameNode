// Package nodeidentity owns this GameNode installation's own durable
// identity: a stable NodeID, a configured display name, and the controlled
// metadata/capability/protocol-version facts a remote controller is allowed
// to learn about this installation (see docs/adr/0006-remote-node-foundation.md).
//
// The identity is generated exactly once and persisted in the local
// database. It intentionally never derives from hostname, IP address, MAC
// address, or database path, so renaming the machine or moving the database
// file cannot change it. This package knows nothing about HTTP, RBAC, or the
// remote node registry (internal/nodes) - it only owns local identity facts.
package nodeidentity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ProtocolVersion is the Remote Node protocol/API version this build speaks.
// It is deliberately independent of the GameNode application version string:
// a controller compares this integer, not a product release number, to
// decide compatibility (see AGENTS.md and docs/adr/0006-remote-node-foundation.md).
const ProtocolVersion = 1

var displayNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]*$`)

// Capability is a stable, typed identifier for a feature this node actually
// supports. Capabilities are advertised, never inferred by a remote caller
// from version numbers, and a capability must only be listed when the
// current build genuinely implements it - see Capabilities below.
type Capability string

const (
	CapabilityNativeRuntime           Capability = "native_runtime"
	CapabilityContainerRuntime        Capability = "container_runtime"
	CapabilityContainerResourceLimits Capability = "container_resource_limits"
	CapabilityConsole                 Capability = "console"
	CapabilityFilesystem              Capability = "filesystem"
	CapabilityMonitoring              Capability = "monitoring"
	CapabilityProvisioning            Capability = "provisioning"
	CapabilityPorts                   Capability = "ports"
)

// Capabilities lists every capability this build of GameNode actually
// implements. It is a fixed, reviewed list - never derived from reflection
// over internal service types (see AGENTS.md: "Do NOT serialize arbitrary
// internal Go type information"). v0.4 Egg Runtime is developed on a
// separate branch and is deliberately not advertised here until it is
// genuinely present on this branch.
func Capabilities() []Capability {
	return []Capability{
		CapabilityNativeRuntime,
		CapabilityContainerRuntime,
		CapabilityContainerResourceLimits,
		CapabilityConsole,
		CapabilityFilesystem,
		CapabilityMonitoring,
		CapabilityProvisioning,
		CapabilityPorts,
	}
}

// Info is the controlled, bounded set of facts this node exposes to an
// authenticated remote caller. It deliberately excludes environment
// variables, filesystem paths, secrets, the database path, the Docker
// socket path, OS credentials, and the process environment (see AGENTS.md
// item 8).
type Info struct {
	NodeID          string       `json:"node_id"`
	DisplayName     string       `json:"display_name"`
	GameNodeVersion string       `json:"gamenode_version"`
	OS              string       `json:"os"`
	Arch            string       `json:"arch"`
	ProtocolVersion int          `json:"protocol_version"`
	Capabilities    []Capability `json:"capabilities"`
	StartedAt       time.Time    `json:"started_at"`
	UptimeSeconds   int64        `json:"uptime_seconds"`
}

// Service owns this installation's persisted identity row.
type Service struct {
	db        *sql.DB
	now       func() time.Time
	startedAt time.Time
	version   string
}

func New(db *sql.DB, version string) *Service {
	return &Service{db: db, now: time.Now, startedAt: time.Now().UTC(), version: version}
}

// Ensure returns this installation's NodeID, generating and persisting one
// exactly once on first call across the lifetime of the database. Later
// calls (including after a restart) always return the same value.
func (s *Service) Ensure(ctx context.Context) (string, error) {
	var nodeID string
	err := s.db.QueryRowContext(ctx, `SELECT node_id FROM node_identity WHERE id='local'`).Scan(&nodeID)
	if err == nil {
		if !validNodeID(nodeID) {
			return "", errors.New("persisted node identity is malformed")
		}
		return nodeID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	nodeID, err = newNodeID()
	if err != nil {
		return "", err
	}
	now := stamp(s.now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO node_identity(id,node_id,display_name,created_at) VALUES('local',?,?,?)`, nodeID, "", now)
	if err != nil {
		// A concurrent Ensure may have won the race; re-read rather than
		// failing startup on a benign unique-constraint collision.
		var existing string
		if lookupErr := s.db.QueryRowContext(ctx, `SELECT node_id FROM node_identity WHERE id='local'`).Scan(&existing); lookupErr == nil && validNodeID(existing) {
			return existing, nil
		}
		return "", err
	}
	return nodeID, nil
}

// DisplayName returns the configured operator-facing name, defaulting to the
// empty string until explicitly set.
func (s *Service) DisplayName(ctx context.Context) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT display_name FROM node_identity WHERE id='local'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func NormalizeDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 100 || !displayNamePattern.MatchString(value) {
		return "", errors.New("display name must be up to 100 ASCII letters, digits, spaces, dots, hyphens, or underscores")
	}
	return value, nil
}

func (s *Service) SetDisplayName(ctx context.Context, name string) (string, error) {
	normalized, err := NormalizeDisplayName(name)
	if err != nil {
		return "", err
	}
	if _, err := s.Ensure(ctx); err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE node_identity SET display_name=? WHERE id='local'`, normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

// LocalInfo builds the controlled Info document for this installation.
func (s *Service) LocalInfo(ctx context.Context) (Info, error) {
	nodeID, err := s.Ensure(ctx)
	if err != nil {
		return Info{}, err
	}
	displayName, err := s.DisplayName(ctx)
	if err != nil {
		return Info{}, err
	}
	now := s.now().UTC()
	return Info{
		NodeID:          nodeID,
		DisplayName:     displayName,
		GameNodeVersion: s.version,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		ProtocolVersion: ProtocolVersion,
		Capabilities:    Capabilities(),
		StartedAt:       s.startedAt,
		UptimeSeconds:   int64(now.Sub(s.startedAt).Seconds()),
	}, nil
}

func validNodeID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil
}

func newNodeID() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
