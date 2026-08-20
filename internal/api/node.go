package api

import (
	"net/http"
	"strings"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/nodes"
	"gamenode/internal/remote"
)

// requireMachineAuth authenticates a Node-facing API request against this
// node's own trusted-caller table. This is a deliberately separate trust
// domain from browser cookie sessions/CSRF (see AGENTS.md items 12/13):
// a controller presents a durable machine credential issued during
// enrollment, never a human session cookie, and this path never checks
// same-origin or CSRF - those concepts do not apply to machine-to-machine
// calls.
func (s *Server) requireMachineAuth(w http.ResponseWriter, r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		unauthorized(w)
		return false
	}
	credential := strings.TrimPrefix(header, prefix)
	ok, err := s.nodes.AuthenticateCaller(r.Context(), credential)
	if err != nil {
		internal(w)
		return false
	}
	if !ok {
		unauthorized(w)
		return false
	}
	return true
}

// nodeInfoHandler implements GET /api/v1/node/info: the controlled,
// bounded identity/version/capability facts this node exposes to an
// authenticated remote caller (see AGENTS.md item 8/18).
func (s *Server) nodeInfoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if !s.requireMachineAuth(w, r) {
		return
	}
	info, err := s.nodeIdentity.LocalInfo(r.Context())
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, info)
}

// nodeHealthHandler implements GET /api/v1/node/health. v0.5A's own health
// is always "healthy" once the process answers - richer degraded states are
// reserved for a future milestone where local runtime health feeds this.
func (s *Server) nodeHealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if !s.requireMachineAuth(w, r) {
		return
	}
	jsonOut(w, http.StatusOK, map[string]string{"status": "healthy"})
}

// nodeStatusHandler exposes a bounded summary of the workloads managed by
// this node to an enrolled controller. Resource values deliberately sum only
// GameNode-managed processes; they are not host-wide utilisation.
func (s *Server) nodeStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if !s.requireMachineAuth(w, r) {
		return
	}
	records, err := s.servers.List(r.Context())
	if err != nil {
		internal(w)
		return
	}
	if len(records) > maxNodeServerListSize {
		records = records[:maxNodeServerListSize]
	}
	status := remote.NodeStatus{}
	for _, record := range records {
		status.Servers.Total++
		switch record.Runtime.CurrentState {
		case "running":
			status.Servers.Running++
		case "stopped":
			status.Servers.Stopped++
		case "crashed":
			status.Servers.Crashed++
		}
		if record.Runtime.ConsoleDetached {
			status.Servers.Detached++
		}
		snapshot, snapshotErr := s.servers.MonitoringSnapshot(r.Context(), record.Server.ID)
		if snapshotErr != nil || record.Runtime.CurrentState != "running" || record.Runtime.ConsoleDetached {
			continue
		}
		status.Workload.CPUPercent += snapshot.CPUPercent
		status.Workload.MemoryBytes += snapshot.MemoryBytes
		status.Workload.SampledServers++
	}
	jsonOut(w, http.StatusOK, status)
}

func (s *Server) nodeCapabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if !s.requireMachineAuth(w, r) {
		return
	}
	info, err := s.nodeIdentity.LocalInfo(r.Context())
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"capabilities": info.Capabilities, "protocol_version": info.ProtocolVersion})
}

type enrollRequest struct {
	PairingToken string `json:"pairing_token"`
}

// nodeEnrollHandler implements POST /api/v1/node/enroll: the one endpoint a
// remote controller may call WITHOUT an existing machine credential, because
// trust is established by a single-use, time-bounded pairing token instead
// (see AGENTS.md item 11/12). A successful call consumes the token and
// issues a brand-new durable machine credential, returned exactly once.
func (s *Server) nodeEnrollHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in enrollRequest
	if !decode(w, r, &in) {
		return
	}
	if err := s.nodes.ConsumePairingToken(r.Context(), in.PairingToken); err != nil {
		// A generic 401 rather than a controlled error code: this endpoint
		// deliberately does not distinguish "unknown token" from "already
		// used" from "expired" to a caller that has not yet proven anything.
		unauthorized(w)
		return
	}
	info, err := s.nodeIdentity.LocalInfo(r.Context())
	if err != nil {
		internal(w)
		return
	}
	credential, err := s.nodes.IssueTrustedCaller(r.Context(), "controller")
	if err != nil {
		internal(w)
		return
	}
	capabilities := make([]string, 0, len(info.Capabilities))
	for _, c := range info.Capabilities {
		capabilities = append(capabilities, string(c))
	}
	s.recordAudit(r, auditInput{action: audit.NodeEnroll, resourceType: audit.Node, resourceName: info.DisplayName, result: audit.Success})
	jsonOut(w, http.StatusOK, map[string]any{
		"node_id": info.NodeID, "display_name": info.DisplayName, "credential": credential,
		"protocol_version": info.ProtocolVersion, "gamenode_version": info.GameNodeVersion,
		"os": info.OS, "arch": info.Arch, "capabilities": capabilities,
	})
}

// nodePairingTokensHandler implements POST /api/v1/node/pairing-tokens: an
// operator of THIS node deliberately generating a one-time enrollment secret
// for a remote controller to consume. Requires ordinary browser
// authentication, Node.Manage, and CSRF - this is a human-initiated action,
// unlike the machine-authenticated endpoints above (see AGENTS.md item 13).
func (s *Server) nodePairingTokensHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	actor, _, ok := s.requireGlobalPermission(w, r, "Node.Manage", true)
	if !ok {
		return
	}
	actorID := actor.ID
	token, expiresAt, err := s.nodes.CreatePairingToken(r.Context(), &actorID)
	if err != nil {
		internal(w)
		return
	}
	s.recordAudit(r, auditInput{action: audit.NodePairingTokenCreate, resourceType: audit.Node, result: audit.Success, actor: &actor})
	jsonOut(w, http.StatusOK, map[string]any{"pairing_token": token, "expires_at": expiresAt.Format(time.RFC3339)})
}

func nodeAuditFailure(err error) (string, string) {
	switch err {
	case nodes.ErrNotFound:
		return "node_not_found", "remote node not found"
	case nodes.ErrDuplicateNodeID:
		return "duplicate_node_id", "this remote node is already enrolled"
	case nodes.ErrDuplicateEndpoint:
		return "duplicate_endpoint", "a remote node with this endpoint is already enrolled"
	case nodes.ErrPairingTokenInvalid:
		return "pairing_token_invalid", "pairing token is invalid, expired, or already used"
	default:
		return "invalid_request", "invalid remote node request"
	}
}
