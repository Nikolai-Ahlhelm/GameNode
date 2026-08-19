package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/placement"
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
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
	candidates = append(candidates, placement.RemoteCandidates(remoteNodes)...)
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

// errRemoteExecuteDispatchFailed is an internal-only sentinel used solely to
// mark recordClusterPlacementExecuteAudit's result as Failure when
// dispatchRemoteProvisioning has already written the caller-facing error
// response itself (see dispatchRemoteProvisioning's doc comment). It is
// never logged, serialized, or exposed to a caller.
var errRemoteExecuteDispatchFailed = errors.New("remote placement execution dispatch failed")

// requireClusterExecutePermission authorizes an explicit placement EXECUTION
// request: it must satisfy every permission a normal provisioning request
// already requires (global Templates.View, tenant-scoped Server.Create - see
// requireTenantProvisionPermission) AND tenant-scoped Cluster.Schedule, since
// this endpoint both computes a placement decision and, unlike
// POST /api/v1/cluster/placement, acts on it. CSRF is checked exactly once
// here.
func (s *Server) requireClusterExecutePermission(w http.ResponseWriter, r *http.Request, tenantID string) (auth.User, bool) {
	actor, _, ok := s.requireGlobalPermission(w, r, "Templates.View", true)
	if !ok {
		return auth.User{}, false
	}
	for _, permission := range [...]string{"Cluster.Schedule", "Server.Create"} {
		allowed, err := s.allowed(r.Context(), actor, permission, rbac.Scope{Type: "tenant", ID: &tenantID})
		if err != nil {
			internal(w)
			return auth.User{}, false
		}
		if !allowed {
			forbidden(w, "permission denied")
			return auth.User{}, false
		}
	}
	if _, err := s.tenants.Get(r.Context(), tenantID); err != nil {
		if errors.Is(err, tenants.ErrTenantNotFound) {
			notFound(w)
		} else {
			internal(w)
		}
		return auth.User{}, false
	}
	return actor, true
}

// clusterPlacementExecuteHandler implements
// POST /api/v1/cluster/placement/execute: the v0.6 container placement
// EXECUTION path. The flow is always: (1) deterministically compute the
// placement decision server-side - a caller can never supply or influence a
// prior decision; (2) if rejected, stop, execute nothing, and audit the
// rejection; (3) validate the requested tenant and (implicitly, through
// placement.Decide) the selected node; (4) for a local target, dispatch
// through the ordinary, already-reviewed provisioning.Service.Start - the
// exact same call the browser-facing POST /api/v1/templates/{id}/provision
// route makes; (5) for a Remote Node target, dispatch through the
// machine-authenticated Node provisioning path
// (dispatchRemoteProvisioning/internal/remote.Client.StartProvisioning).
// internal/placement is never given (and never needs) a Docker or runtime
// handle - it only ever selects a node; this handler is the one and only
// place a selected node's identity turns into an actual provisioning call,
// and that call always goes through provisioning.Service, on the local node
// or the remote one, never a second container-lifecycle implementation.
// Exactly one ClusterPlacementExecute audit event is recorded per request.
func (s *Server) clusterPlacementExecuteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var in nodeProvisioningRequest
	if !decodeNodeProvisioningInput(w, r, &in) {
		return
	}
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID == "" {
		tenantID = tenants.DefaultTenantID
	}
	runtimeType := strings.TrimSpace(in.RuntimeType)
	if runtimeType == "" {
		runtimeType = "native"
	}
	if runtimeType != "native" && runtimeType != "container" {
		bad(w, "runtime_type must be 'native' or 'container'")
		return
	}
	actor, ok := s.requireClusterExecutePermission(w, r, tenantID)
	if !ok {
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
	if decision.Rejected {
		s.recordClusterPlacementExecuteAudit(r, actor, tenantID, runtimeType, decision, "", nil)
		errorOut(w, http.StatusUnprocessableEntity, "no_eligible_node", "no eligible node is available for this placement request")
		return
	}

	switch decision.Selected.Kind {
	case placement.NodeLocal:
		job, err := s.provisioning.Start(r.Context(), provisioning.Request{
			TemplateID: in.TemplateID, ServerName: in.ServerName, DirectoryName: in.DirectoryName, Values: in.Variables,
			ActorUserID: actor.ID, ActorUsername: actor.Username, RecoverExisting: in.RecoverExisting,
			TenantID: tenantID, RuntimeType: runtimeType, Image: in.Image,
			MemoryLimitBytes: in.MemoryLimitBytes, CPULimitMillis: in.CPULimitMillis, PIDsLimit: in.PIDsLimit, TmpfsSizeBytes: in.TmpfsSizeBytes,
		})
		if err != nil {
			s.recordClusterPlacementExecuteAudit(r, actor, tenantID, runtimeType, decision, "", err)
			provisioningError(w, err)
			return
		}
		s.recordClusterPlacementExecuteAudit(r, actor, tenantID, runtimeType, decision, job.ID, nil)
		jsonOut(w, http.StatusAccepted, map[string]any{"decision": toPublicDecision(decision), "job": job})
	case placement.NodeRemote:
		req := remote.ProvisioningRequest{
			TemplateID: in.TemplateID, ServerName: in.ServerName, DirectoryName: in.DirectoryName, Variables: in.Variables,
			RecoverExisting: in.RecoverExisting, TenantID: tenantID, RuntimeType: runtimeType, Image: in.Image,
			MemoryLimitBytes: in.MemoryLimitBytes, CPULimitMillis: in.CPULimitMillis, PIDsLimit: in.PIDsLimit, TmpfsSizeBytes: in.TmpfsSizeBytes,
		}
		job, dispatched := s.dispatchRemoteProvisioning(w, r, decision.Selected.NodeID, req)
		if !dispatched {
			// dispatchRemoteProvisioning already wrote the caller-facing
			// error response; only the audit record remains.
			s.recordClusterPlacementExecuteAudit(r, actor, tenantID, runtimeType, decision, "", errRemoteExecuteDispatchFailed)
			return
		}
		s.recordClusterPlacementExecuteAudit(r, actor, tenantID, runtimeType, decision, job.ID, nil)
		jsonOut(w, http.StatusAccepted, map[string]any{"decision": toPublicDecision(decision), "job": job})
	default:
		internal(w)
	}
}

func (s *Server) recordClusterPlacementExecuteAudit(r *http.Request, actor auth.User, tenantID, runtimeType string, decision placement.Decision, jobID string, dispatchErr error) {
	metadata := map[string]any{"tenant_id": tenantID, "runtime_type": runtimeType}
	result := audit.Success
	if decision.Rejected {
		result = audit.Failure
		metadata["reason"] = string(decision.Reason)
	} else {
		metadata["selected_node_id"] = decision.Selected.NodeID
		metadata["selected_kind"] = string(decision.Selected.Kind)
		metadata["execution"] = string(decision.Execution)
		if jobID != "" {
			metadata["job_id"] = jobID
		}
		if dispatchErr != nil {
			result = audit.Failure
		}
	}
	metaJSON, _ := json.Marshal(metadata)
	tenantResourceID := tenantID
	in := auditInput{
		action: audit.ClusterPlacementExecute, resourceType: audit.Cluster, resourceID: &tenantResourceID,
		resourceName: tenantID, result: result, metadata: metaJSON, actor: &actor,
	}
	if dispatchErr != nil {
		in.errorCode = "placement_execution_failed"
		in.errorSummary = "cluster placement execution failed"
	}
	s.recordAudit(r, in)
}
