package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/nodes"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
)

// publicRemoteNode is the projection of nodes.RemoteNode returned by the
// controller-facing API. It never includes Credential - the machine secret
// this controller presents to the remote node - which is written once at
// enrollment time and never read back through any API response (see
// AGENTS.md item 11).
type publicRemoteNode struct {
	ID              string   `json:"id"`
	NodeID          string   `json:"node_id"`
	DisplayName     string   `json:"display_name"`
	Endpoint        string   `json:"endpoint"`
	ProtocolVersion int      `json:"protocol_version"`
	GameNodeVersion string   `json:"gamenode_version"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Capabilities    []string `json:"capabilities"`
	Enabled         bool     `json:"enabled"`
	TrustStatus     string   `json:"trust_status"`
	LastSeenAt      *string  `json:"last_seen_at,omitempty"`
	LastHealth      string   `json:"last_health"`
	LastErrorCode   string   `json:"last_error_code,omitempty"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	// Compatibility summarizes protocol_version against this controller's
	// own build (see AGENTS.md item 32): it is derived, never a stored
	// field, so it always reflects the current binary's protocol version.
	Compatibility string `json:"compatibility"`
}

func (s *Server) toPublicRemoteNode(n nodes.RemoteNode) publicRemoteNode {
	out := publicRemoteNode{
		ID: n.ID, NodeID: n.NodeID, DisplayName: n.DisplayName, Endpoint: n.Endpoint,
		ProtocolVersion: n.ProtocolVersion, GameNodeVersion: n.GameNodeVersion, OS: n.OS, Arch: n.Arch,
		Capabilities: n.Capabilities, Enabled: n.Enabled, TrustStatus: string(n.TrustStatus),
		LastHealth: string(n.LastHealth), LastErrorCode: n.LastErrorCode,
		CreatedAt: n.CreatedAt.Format(rfc3339), UpdatedAt: n.UpdatedAt.Format(rfc3339),
	}
	if n.LastSeenAt != nil {
		v := n.LastSeenAt.Format(rfc3339)
		out.LastSeenAt = &v
	}
	out.Compatibility = compatibilityOf(n.ProtocolVersion)
	return out
}

const rfc3339 = "2006-01-02T15:04:05.999999999Z07:00"

func compatibilityOf(remoteProtocolVersion int) string {
	switch {
	case remoteProtocolVersion == localProtocolVersion:
		return "compatible"
	case remoteProtocolVersion == 0:
		return "unknown"
	case remoteProtocolVersion < localProtocolVersion:
		return "limited_capabilities"
	default:
		return "incompatible"
	}
}

func (s *Server) recordNodeAudit(r *http.Request, actor auth.User, action, id, name string, metadata map[string]any, err error) {
	var resourceID *string
	if id != "" {
		resourceID = &id
	}
	in := auditInput{action: action, resourceType: audit.Node, resourceID: resourceID, resourceName: name, result: audit.Success, actor: &actor}
	if err != nil {
		in.result = audit.Failure
		in.errorCode, in.errorSummary = nodeAuditFailure(err)
	} else if metadata != nil {
		in.metadata, _ = json.Marshal(metadata)
	}
	s.recordAudit(r, in)
}

func remoteNodeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nodes.ErrNotFound):
		notFound(w)
	case errors.Is(err, nodes.ErrDuplicateNodeID), errors.Is(err, nodes.ErrDuplicateEndpoint):
		code, message := nodeAuditFailure(err)
		errorOut(w, http.StatusConflict, code, message)
	case errors.Is(err, nodes.ErrPairingTokenInvalid):
		errorOut(w, http.StatusUnauthorized, "pairing_token_invalid", "pairing token is invalid, expired, or already used")
	default:
		var remoteErr *remote.Error
		if errors.As(err, &remoteErr) {
			errorOut(w, http.StatusBadGateway, string(remoteErr.Kind), remoteErrorMessage(remoteErr.Kind))
			return
		}
		bad(w, "invalid remote node request")
	}
}

func remoteErrorMessage(kind remote.Kind) string {
	switch kind {
	case remote.KindUnreachable:
		return "the remote node could not be reached"
	case remote.KindAuthenticationFailed:
		return "the remote node rejected the pairing token or credential"
	case remote.KindProtocolIncompatible:
		return "the remote node's protocol version is incompatible"
	case remote.KindOversizedResponse:
		return "the remote node's response exceeded the size limit"
	case remote.KindResourceNotFound:
		return "the requested resource was not found on the remote node"
	case remote.KindResourceConflict:
		return "the remote node rejected the request because of the resource's current state"
	default:
		return "the remote node returned an unexpected response"
	}
}

// remoteNodesHandler implements GET/POST /api/v1/remote-nodes: the
// controller-facing registry of Remote Nodes this installation manages.
// This is ordinary browser-authenticated, RBAC- and CSRF-protected API - a
// completely separate trust domain from the machine-authenticated
// /api/v1/node/* endpoints above (see AGENTS.md item 13).
func (s *Server) remoteNodesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Node.View", false); !ok {
			return
		}
		list, err := s.nodes.List(r.Context())
		if err != nil {
			internal(w)
			return
		}
		out := make([]publicRemoteNode, 0, len(list))
		for _, n := range list {
			out = append(out, s.toPublicRemoteNode(n))
		}
		jsonOut(w, http.StatusOK, map[string]any{"remote_nodes": out})
	case http.MethodPost:
		s.enrollRemoteNode(w, r)
	default:
		method(w)
	}
}

type enrollRemoteNodeRequest struct {
	Endpoint     string `json:"endpoint"`
	PairingToken string `json:"pairing_token"`
	DisplayName  string `json:"display_name"`
}

// enrollRemoteNode implements the controller side of enrollment: validate
// the operator-supplied endpoint, call the remote node's own
// /api/v1/node/enroll with the pairing token the operator obtained out of
// band, and persist the returned machine credential. The pairing token and
// credential are never logged or included in audit metadata (see AGENTS.md
// item 11/26).
func (s *Server) enrollRemoteNode(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := s.requireGlobalPermission(w, r, "Node.Manage", true)
	if !ok {
		return
	}
	var in enrollRemoteNodeRequest
	if !decode(w, r, &in) {
		return
	}
	endpoint, err := remote.ValidateEndpoint(in.Endpoint)
	if err != nil {
		s.recordNodeAudit(r, actor, audit.NodeEnroll, "", strings.TrimSpace(in.DisplayName), nil, err)
		bad(w, "endpoint is invalid: "+err.Error())
		return
	}
	if strings.TrimSpace(in.PairingToken) == "" {
		bad(w, "pairing token is required")
		return
	}
	result, err := s.remoteClient.Enroll(r.Context(), endpoint, in.PairingToken)
	if err != nil {
		s.recordNodeAudit(r, actor, audit.NodeEnroll, "", strings.TrimSpace(in.DisplayName), nil, err)
		remoteNodeError(w, err)
		return
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = result.DisplayName
	}
	n, err := s.nodes.CreateEnrolled(r.Context(), nodes.CreateEnrolledInput{
		DisplayName: displayName, Endpoint: endpoint, Credential: result.Credential, NodeID: result.NodeID,
		ProtocolVersion: result.ProtocolVersion, GameNodeVersion: result.GameNodeVersion, OS: result.OS, Arch: result.Arch,
		Capabilities: result.Capabilities,
	})
	if err != nil {
		s.recordNodeAudit(r, actor, audit.NodeEnroll, "", displayName, nil, err)
		remoteNodeError(w, err)
		return
	}
	s.recordNodeAudit(r, actor, audit.NodeEnroll, n.ID, n.DisplayName, map[string]any{"endpoint": n.Endpoint, "node_id": n.NodeID}, nil)
	jsonOut(w, http.StatusCreated, map[string]any{"remote_node": s.toPublicRemoteNode(n)})
}

// remoteNodeHandler implements /api/v1/remote-nodes/{id} and its
// sub-resource /refresh.
func (s *Server) remoteNodeHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/remote-nodes/")
	parts := strings.Split(path, "/")
	if parts[0] == "" || len(parts) > 6 {
		notFound(w)
		return
	}
	id := parts[0]
	if len(parts) >= 2 && parts[1] == "servers" {
		s.remoteServersRouter(w, r, id, parts[2:])
		return
	}
	if len(parts) == 2 && parts[1] == "status" {
		s.remoteNodeStatusHandler(w, r, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "provisioning" {
		s.remoteNodeProvisioningHandler(w, r, id, parts[2:])
		return
	}
	if len(parts) > 2 {
		notFound(w)
		return
	}
	if len(parts) == 2 {
		if parts[1] != "refresh" {
			notFound(w)
			return
		}
		s.refreshRemoteNode(w, r, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Node.View", false); !ok {
			return
		}
		n, err := s.nodes.Get(r.Context(), id)
		if err != nil {
			remoteNodeError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"remote_node": s.toPublicRemoteNode(n)})
	case http.MethodPatch:
		s.updateRemoteNode(w, r, id)
	case http.MethodDelete:
		actor, _, ok := s.requireGlobalPermission(w, r, "Node.Manage", true)
		if !ok {
			return
		}
		name := ""
		if existing, err := s.nodes.Get(r.Context(), id); err == nil {
			name = existing.DisplayName
		}
		if err := s.nodes.Delete(r.Context(), id); err != nil {
			s.recordNodeAudit(r, actor, audit.NodeRemove, id, name, nil, err)
			remoteNodeError(w, err)
			return
		}
		s.recordNodeAudit(r, actor, audit.NodeRemove, id, name, nil, nil)
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}

// remoteNodeStatusHandler exposes a remote node's aggregate to browser
// users. Global remote-server and monitoring rights are mandatory because a
// tenant-scoped grant must never learn another tenant's node-wide totals.
func (s *Server) remoteNodeStatusHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	u, _, ok := s.requireGlobalPermission(w, r, "Node.View", false)
	if !ok {
		return
	}
	for _, permission := range []string{"RemoteServer.View", "RemoteMonitoring.View"} {
		allowed, err := s.allowed(r.Context(), u, permission, rbac.Scope{Type: "global"})
		if err != nil {
			internal(w)
			return
		}
		if !allowed {
			forbidden(w, "permission denied")
			return
		}
	}
	n, ok := s.requireEnabledRemoteNode(w, r, id, "remote_server_management")
	if !ok {
		return
	}
	status, err := s.remoteClient.GetNodeStatus(r.Context(), n.Endpoint, n.Credential)
	if err != nil {
		remoteNodeError(w, err)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"remote_node_status": status})
}

type updateRemoteNodeRequest struct {
	DisplayName *string `json:"display_name"`
	Enabled     *bool   `json:"enabled"`
}

func (s *Server) updateRemoteNode(w http.ResponseWriter, r *http.Request, id string) {
	actor, _, ok := s.requireGlobalPermission(w, r, "Node.Manage", true)
	if !ok {
		return
	}
	var in updateRemoteNodeRequest
	if !decode(w, r, &in) {
		return
	}
	var n nodes.RemoteNode
	var err error
	if in.DisplayName != nil {
		n, err = s.nodes.UpdateDisplayName(r.Context(), id, *in.DisplayName)
		if err != nil {
			s.recordNodeAudit(r, actor, audit.NodeUpdate, id, "", nil, err)
			remoteNodeError(w, err)
			return
		}
		s.recordNodeAudit(r, actor, audit.NodeUpdate, id, n.DisplayName, map[string]any{"display_name": n.DisplayName}, nil)
	}
	if in.Enabled != nil {
		n, err = s.nodes.SetEnabled(r.Context(), id, *in.Enabled)
		if err != nil {
			action := audit.NodeEnable
			if !*in.Enabled {
				action = audit.NodeDisable
			}
			s.recordNodeAudit(r, actor, action, id, "", nil, err)
			remoteNodeError(w, err)
			return
		}
		action := audit.NodeEnable
		if !*in.Enabled {
			action = audit.NodeDisable
		}
		s.recordNodeAudit(r, actor, action, id, n.DisplayName, map[string]any{"enabled": n.Enabled}, nil)
	}
	if in.DisplayName == nil && in.Enabled == nil {
		n, err = s.nodes.Get(r.Context(), id)
		if err != nil {
			remoteNodeError(w, err)
			return
		}
	}
	jsonOut(w, http.StatusOK, map[string]any{"remote_node": s.toPublicRemoteNode(n)})
}

// refreshRemoteNode implements POST /api/v1/remote-nodes/{id}/refresh: an
// operator-triggered, bounded, single-node status refresh. It is gated by
// Node.View (a read against the remote node, not a registry mutation) and
// is intentionally not audited - see the periodic refresher in
// internal/api/node_refresh.go for the identical, unaudited status update
// path used by the background heartbeat.
func (s *Server) refreshRemoteNode(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Node.View", true); !ok {
		return
	}
	n, err := s.nodes.Get(r.Context(), id)
	if err != nil {
		remoteNodeError(w, err)
		return
	}
	s.refreshOne(r.Context(), n)
	updated, err := s.nodes.Get(r.Context(), id)
	if err != nil {
		remoteNodeError(w, err)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"remote_node": s.toPublicRemoteNode(updated)})
}
