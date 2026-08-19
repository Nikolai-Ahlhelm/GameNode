package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/nodes"
	"gamenode/internal/placement"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
	"gamenode/internal/servers"
	"gamenode/internal/tenants"
)

// publicCandidate is the API projection of placement.NodeCandidate. It never
// includes anything about another tenant's servers - capacity is a
// node-wide count, not a per-tenant breakdown, so there is nothing tenant-
// specific to leak here (see placement.LocalCandidate's doc comment).
type publicCandidate struct {
	NodeID        string   `json:"node_id"`
	DisplayName   string   `json:"display_name"`
	Kind          string   `json:"kind"`
	Enabled       bool     `json:"enabled"`
	Healthy       bool     `json:"healthy"`
	Capabilities  []string `json:"capabilities"`
	CapacityKnown bool     `json:"capacity_known"`
	UsedServers   int      `json:"used_servers,omitempty"`
	MaxServers    int      `json:"max_servers,omitempty"`
	Available     int      `json:"available,omitempty"`
}

func toPublicCandidate(c placement.NodeCandidate) publicCandidate {
	out := publicCandidate{
		NodeID: c.NodeID, DisplayName: c.DisplayName, Kind: string(c.Kind), Enabled: c.Enabled, Healthy: c.Healthy,
		Capabilities: c.Capabilities, CapacityKnown: c.CapacityKnown,
	}
	if c.CapacityKnown {
		out.UsedServers = c.UsedServers
		out.MaxServers = c.MaxServers
		out.Available = c.Available()
	}
	return out
}

type publicCandidateResult struct {
	NodeID      string `json:"node_id"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
	Selected    bool   `json:"selected"`
	Reason      string `json:"reason,omitempty"`
}

type publicDecision struct {
	TenantID   string                  `json:"tenant_id"`
	Rejected   bool                    `json:"rejected"`
	Reason     string                  `json:"reason,omitempty"`
	Selected   *publicCandidate        `json:"selected,omitempty"`
	Execution  string                  `json:"execution,omitempty"`
	Candidates []publicCandidateResult `json:"candidates"`
}

// buildPlacementCandidates assembles the full candidate list (this
// installation plus every enrolled Remote Node) from already-existing,
// read-only data sources: servers.Service.List for local usage,
// nodeidentity.LocalInfo for this node's own identity/capabilities, and
// nodes.Service.List for the Remote Node registry's last-known health and
// advertised capabilities. It performs no remote network call itself - the
// registry rows already reflect the periodic bounded heartbeat
// (internal/api/node_refresh.go).
func (s *Server) buildPlacementCandidates(ctx context.Context) ([]placement.NodeCandidate, error) {
	allServers, err := s.servers.List(ctx)
	if err != nil {
		return nil, err
	}
	info, err := s.nodeIdentity.LocalInfo(ctx)
	if err != nil {
		return nil, err
	}
	remoteNodes, err := s.nodes.List(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]placement.NodeCandidate, 0, 1+len(remoteNodes))
	candidates = append(candidates, placement.LocalCandidate(info, allServers))
	remoteCandidates := placement.RemoteCandidates(remoteNodes)
	for i := range remoteCandidates {
		for _, n := range remoteNodes {
			if n.ID != remoteCandidates[i].NodeID || !n.Enabled || n.LastHealth != nodes.HealthReachable || !containsCapability(n.Capabilities, "remote_server_management") {
				continue
			}
			list, err := s.remoteClient.ListServers(ctx, n.Endpoint, n.Credential)
			if err == nil {
				remoteCandidates[i].CapacityKnown = true
				remoteCandidates[i].UsedServers = len(list)
				remoteCandidates[i].MaxServers = placement.DefaultMaxServersPerNode
			}
			break
		}
	}
	candidates = append(candidates, remoteCandidates...)
	return candidates, nil
}

// requireClusterTenantPermission checks the given permission effective for
// the requested tenant (global or tenant-scoped assignment; see
// rbac.AllowedScopes for Cluster.View/Cluster.Schedule) and that the tenant
// itself exists. It never leaks whether a tenant exists to a caller who
// lacks the permission entirely - permission is checked first, exactly like
// every other tenant-scoped route in this package (see provisioning.go).
func (s *Server) requireClusterTenantPermission(w http.ResponseWriter, r *http.Request, permission, tenantID string, csrfRequired bool) (auth.User, bool) {
	u, _, ok := s.requireAuth(w, r, csrfRequired)
	if !ok {
		return auth.User{}, false
	}
	allowed, err := s.allowed(r.Context(), u, permission, rbac.Scope{Type: "tenant", ID: &tenantID})
	if err != nil {
		internal(w)
		return auth.User{}, false
	}
	if !allowed {
		forbidden(w, "permission denied")
		return auth.User{}, false
	}
	if _, err := s.tenants.Get(r.Context(), tenantID); err != nil {
		if errors.Is(err, tenants.ErrTenantNotFound) {
			notFound(w)
		} else {
			internal(w)
		}
		return auth.User{}, false
	}
	return u, true
}

// clusterCapacityHandler implements GET /api/v1/cluster/capacity?tenant_id=…:
// a read-only listing of every placement candidate (this node plus every
// enrolled Remote Node) and its capacity, for the given tenant's scope. It
// never mutates anything and is not audited, matching the "no audit for
// routine reads" convention used by Node.View's refresh/list routes.
func (s *Server) clusterCapacityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		tenantID = tenants.DefaultTenantID
	}
	if _, ok := s.requireClusterTenantPermission(w, r, "Cluster.View", tenantID, false); !ok {
		return
	}
	candidates, err := s.buildPlacementCandidates(r.Context())
	if err != nil {
		internal(w)
		return
	}
	out := make([]publicCandidate, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, toPublicCandidate(c))
	}
	jsonOut(w, http.StatusOK, map[string]any{"tenant_id": tenantID, "candidates": out})
}

type clusterPlacementRequest struct {
	TenantID    string `json:"tenant_id"`
	RuntimeType string `json:"runtime_type"`
}

// clusterPlacementHandler implements POST /api/v1/cluster/placement: compute
// a deterministic placement DECISION for one new server of the requested
// runtime type in the requested tenant. It never creates, starts, or
// otherwise mutates a server anywhere - see placement.Decide's doc comment
// and docs/adr/0009-cluster-scheduling-decision-vs-execution.md. Every
// decision (accepted or rejected) is audited exactly once.
func (s *Server) clusterPlacementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in clusterPlacementRequest
	if !decode(w, r, &in) {
		return
	}
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		tenantID = tenants.DefaultTenantID
	}
	actor, ok := s.requireClusterTenantPermission(w, r, "Cluster.Schedule", tenantID, true)
	if !ok {
		return
	}
	runtimeType := strings.TrimSpace(in.RuntimeType)
	if runtimeType == "" {
		runtimeType = "native"
	}
	if runtimeType != "native" && runtimeType != "container" {
		bad(w, "runtime_type must be 'native' or 'container'")
		return
	}
	candidates, err := s.buildPlacementCandidates(r.Context())
	if err != nil {
		internal(w)
		return
	}
	decision := placement.Decide(placement.Request{
		TenantID:             tenantID,
		RequiredCapabilities: []string{placement.RuntimeCapability(runtimeType)},
		Candidates:           candidates,
	})
	s.recordClusterPlacementAudit(r, actor, tenantID, runtimeType, decision)
	jsonOut(w, http.StatusOK, map[string]any{"decision": toPublicDecision(decision)})
}

type clusterPlacementExecuteRequest struct {
	TenantID    string                   `json:"tenant_id"`
	RuntimeType string                   `json:"runtime_type"`
	Server      remote.CreateServerInput `json:"server"`
}

// clusterPlacementExecuteHandler is the explicit execution step following a
// placement decision. It recomputes placement from current node state and
// then delegates creation to the selected node: local servers.Service.Create
// or the enrolled node's typed machine API. The decision endpoint above stays
// read-only and never turns into an implicit orchestrator.
func (s *Server) clusterPlacementExecuteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in clusterPlacementExecuteRequest
	if !decode(w, r, &in) {
		return
	}
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		tenantID = tenants.DefaultTenantID
	}
	actor, ok := s.requireClusterTenantPermission(w, r, "Cluster.Schedule", tenantID, true)
	if !ok {
		return
	}
	runtimeType := strings.TrimSpace(in.RuntimeType)
	if runtimeType == "" {
		runtimeType = strings.TrimSpace(in.Server.RuntimeType)
	}
	if runtimeType == "" {
		runtimeType = "native"
	}
	if runtimeType != "native" && runtimeType != "container" {
		bad(w, "runtime_type must be 'native' or 'container'")
		return
	}
	if runtimeType == "container" {
		bad(w, "container placement execution requires the existing typed provisioning contract")
		return
	}
	if in.Server.TenantID == "" {
		in.Server.TenantID = tenantID
	}
	if in.Server.TenantID != tenantID {
		forbidden(w, "server tenant does not match placement tenant")
		return
	}
	in.Server.RuntimeType = runtimeType
	candidates, err := s.buildPlacementCandidates(r.Context())
	if err != nil {
		internal(w)
		return
	}
	decision := placement.Decide(placement.Request{TenantID: tenantID, RequiredCapabilities: []string{placement.RuntimeCapability(runtimeType)}, Candidates: candidates})
	if decision.Rejected || decision.Selected == nil {
		s.recordClusterPlacementAudit(r, actor, tenantID, runtimeType, decision)
		jsonOut(w, http.StatusConflict, map[string]any{"decision": toPublicDecision(decision)})
		return
	}
	if decision.Selected.Kind == placement.NodeLocal {
		local := servers.Server{TenantID: tenantID, CreationMode: servers.CreationCustom, Name: in.Server.Name, Description: in.Server.Description, WorkingDirectory: in.Server.WorkingDirectory, Executable: in.Server.Executable, Arguments: in.Server.Arguments, EnvironmentVariables: in.Server.EnvironmentVariables, RuntimeType: runtimeType, AutoStart: in.Server.AutoStart, RestartPolicy: in.Server.RestartPolicy, StopMethod: in.Server.StopMethod, StopCommand: in.Server.StopCommand, StopTimeoutSeconds: in.Server.StopTimeoutSeconds}
		record, err := s.servers.Create(r.Context(), local)
		if err != nil {
			s.recordClusterPlacementExecutionAudit(r, actor, tenantID, decision, err)
			serverError(w, err, false)
			return
		}
		s.recordClusterPlacementExecutionAudit(r, actor, tenantID, decision, nil)
		jsonOut(w, http.StatusCreated, map[string]any{"decision": toPublicDecision(decision), "node_id": "local", "server_id": record.Server.ID, "server": record.Server})
		return
	}
	node, err := s.nodes.Get(r.Context(), decision.Selected.NodeID)
	if err != nil || !node.Enabled || node.LastHealth != nodes.HealthReachable {
		errorOut(w, http.StatusConflict, "node_unavailable", "selected remote node is no longer available")
		return
	}
	created, err := s.remoteClient.CreateServer(r.Context(), node.Endpoint, node.Credential, in.Server)
	if err != nil {
		s.recordClusterPlacementExecutionAudit(r, actor, tenantID, decision, err)
		remoteServerError(w, err)
		return
	}
	s.recordClusterPlacementExecutionAudit(r, actor, tenantID, decision, nil)
	jsonOut(w, http.StatusCreated, map[string]any{"decision": toPublicDecision(decision), "node_id": node.ID, "server": created})
}

func toPublicDecision(d placement.Decision) publicDecision {
	out := publicDecision{TenantID: d.TenantID, Rejected: d.Rejected, Reason: string(d.Reason), Execution: string(d.Execution)}
	if d.Selected != nil {
		selected := toPublicCandidate(*d.Selected)
		out.Selected = &selected
	}
	out.Candidates = make([]publicCandidateResult, 0, len(d.Candidates))
	for _, c := range d.Candidates {
		out.Candidates = append(out.Candidates, publicCandidateResult{NodeID: c.NodeID, DisplayName: c.DisplayName, Kind: string(c.Kind), Selected: c.Selected, Reason: string(c.Reason)})
	}
	return out
}

func (s *Server) recordClusterPlacementAudit(r *http.Request, actor auth.User, tenantID, runtimeType string, decision placement.Decision) {
	result := audit.Success
	metadata := map[string]any{"tenant_id": tenantID, "runtime_type": runtimeType}
	if decision.Rejected {
		result = audit.Failure
		metadata["reason"] = string(decision.Reason)
	} else {
		metadata["selected_node_id"] = decision.Selected.NodeID
		metadata["selected_kind"] = string(decision.Selected.Kind)
		metadata["execution"] = string(decision.Execution)
	}
	metaJSON, _ := json.Marshal(metadata)
	tenantResourceID := tenantID
	s.recordAudit(r, auditInput{
		action: audit.ClusterPlacementDecision, resourceType: audit.Cluster, resourceID: &tenantResourceID,
		resourceName: tenantID, result: result, metadata: metaJSON, actor: &actor,
	})
}

func (s *Server) recordClusterPlacementExecutionAudit(r *http.Request, actor auth.User, tenantID string, decision placement.Decision, err error) {
	resourceID := tenantID
	in := auditInput{action: audit.ClusterPlacementExecute, resourceType: audit.Cluster, resourceID: &resourceID, resourceName: tenantID, actor: &actor, result: audit.Success}
	if decision.Selected != nil {
		metadata, _ := json.Marshal(map[string]any{"tenant_id": tenantID, "node_id": decision.Selected.NodeID, "kind": decision.Selected.Kind})
		in.metadata = metadata
	}
	if err != nil {
		in.result = audit.Failure
		in.errorCode, in.errorSummary = auditFailure(err)
	}
	s.recordAudit(r, in)
}
