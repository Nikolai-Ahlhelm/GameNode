package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
	"gamenode/internal/tenants"
)

// requireTenantProvisionPermission is the tenant-scoped counterpart of
// requireProvisionPermission (internal/api/provisioning.go), used wherever a
// caller is about to actually create a server for a specific tenant rather
// than only read template metadata: global Templates.View (can this
// installation provision this kind of template at all) AND Server.Create
// effective for the requested tenant (global or tenant-scoped assignment).
// This mirrors startProvisioning's identical two-step check exactly, so the
// remote-provisioning proxy and the placement execute endpoint enforce the
// same authorization a local provisioning request already enforces - no new
// permission tier is introduced for remote/container placement.
func (s *Server) requireTenantProvisionPermission(w http.ResponseWriter, r *http.Request, tenantID string, csrf bool) (auth.User, bool) {
	actor, _, ok := s.requireGlobalPermission(w, r, "Templates.View", csrf)
	if !ok {
		return auth.User{}, false
	}
	allowed, err := s.allowed(r.Context(), actor, "Server.Create", rbac.Scope{Type: "tenant", ID: &tenantID})
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
	return actor, true
}

// dispatchRemoteProvisioning is the one place the controller forwards a
// provisioning request to a Remote Node's machine-authenticated
// POST /api/v1/node/provisioning. It never interprets, validates, or
// mutates container/Egg data itself - the target node's own
// provisioning.Service remains the sole authority for template/Egg
// validation, the image allowlist, resource limits, its tenant/filesystem
// sandbox, the container installer, and job persistence. This function
// performs no RBAC/CSRF/audit itself - every caller (the direct proxy
// handler below, and the cluster placement execute handler) does that once,
// at its own call site, so exactly one audit record is written per actual
// caller-facing request.
func (s *Server) dispatchRemoteProvisioning(w http.ResponseWriter, r *http.Request, nodeID string, req remote.ProvisioningRequest) (provisioning.Job, bool) {
	node, err := s.nodes.Get(r.Context(), nodeID)
	if err != nil {
		remoteNodeError(w, err)
		return provisioning.Job{}, false
	}
	if !node.Enabled {
		errorOut(w, http.StatusBadGateway, string(remote.KindUnreachable), "the remote node is disabled")
		return provisioning.Job{}, false
	}
	endpoint, credential, err := s.nodes.Credential(r.Context(), nodeID)
	if err != nil {
		remoteNodeError(w, err)
		return provisioning.Job{}, false
	}
	job, err := s.remoteClient.StartProvisioning(r.Context(), endpoint, credential, req)
	if err != nil {
		remoteProvisioningError(w, err)
		return provisioning.Job{}, false
	}
	return job, true
}

// remoteProvisioningError translates an error from the Remote Node
// provisioning client into the same shape a local provisioning failure
// already produces (see provisioningError in internal/api/provisioning.go):
// a *remote.ProvisioningError carries the target node's own typed
// provisioning error code/message straight through, and every other error
// falls back to the generic transport classification in remoteNodeError.
func remoteProvisioningError(w http.ResponseWriter, err error) {
	var provErr *remote.ProvisioningError
	if errors.As(err, &provErr) {
		status := provErr.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		errorOut(w, status, provErr.Code, provErr.Message)
		return
	}
	remoteNodeError(w, err)
}

// remoteNodeProvisioningHandler implements the controller-side proxy for
// container/Egg provisioning on a Remote Node:
//
//	POST /api/v1/remote-nodes/{nodeID}/provisioning
//	GET  /api/v1/remote-nodes/{nodeID}/provisioning/{jobID}
//	POST /api/v1/remote-nodes/{nodeID}/provisioning/{jobID}/cancel
//
// The controller only ever authenticates, checks RBAC/CSRF/tenant scope,
// forwards the already-typed request to the target node's machine-
// authenticated Node API, returns the bounded job status it gets back, and
// writes one audit record - it never interprets container data itself and
// never talks to Docker or the remote node's database directly (see
// docs/adr/0009-cluster-scheduling-decision-vs-execution.md).
func (s *Server) remoteNodeProvisioningHandler(w http.ResponseWriter, r *http.Request, nodeID string, rest []string) {
	switch len(rest) {
	case 0:
		s.startRemoteProvisioningHandler(w, r, nodeID)
	case 1:
		s.remoteProvisioningJobStatusHandler(w, r, nodeID, rest[0])
	case 2:
		if rest[1] != "cancel" {
			notFound(w)
			return
		}
		s.remoteProvisioningJobCancelHandler(w, r, nodeID, rest[0])
	default:
		notFound(w)
	}
}

func (s *Server) startRemoteProvisioningHandler(w http.ResponseWriter, r *http.Request, nodeID string) {
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
	actor, ok := s.requireTenantProvisionPermission(w, r, tenantID, true)
	if !ok {
		return
	}
	req := remote.ProvisioningRequest{
		TemplateID: in.TemplateID, ServerName: in.ServerName, DirectoryName: in.DirectoryName, Variables: in.Variables,
		RecoverExisting: in.RecoverExisting, TenantID: tenantID, RuntimeType: in.RuntimeType, Image: in.Image,
		MemoryLimitBytes: in.MemoryLimitBytes, CPULimitMillis: in.CPULimitMillis, PIDsLimit: in.PIDsLimit, TmpfsSizeBytes: in.TmpfsSizeBytes,
	}
	job, ok := s.dispatchRemoteProvisioning(w, r, nodeID, req)
	s.recordRemoteProvisioningAudit(r, actor, nodeID, tenantID, req.RuntimeType, job, ok)
	if !ok {
		return
	}
	jsonOut(w, http.StatusAccepted, job)
}

func (s *Server) remoteProvisioningJobStatusHandler(w http.ResponseWriter, r *http.Request, nodeID, jobID string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		tenantID = tenants.DefaultTenantID
	}
	if _, ok := s.requireTenantProvisionPermission(w, r, tenantID, false); !ok {
		return
	}
	node, err := s.nodes.Get(r.Context(), nodeID)
	if err != nil {
		remoteNodeError(w, err)
		return
	}
	if !node.Enabled {
		errorOut(w, http.StatusBadGateway, string(remote.KindUnreachable), "the remote node is disabled")
		return
	}
	endpoint, credential, err := s.nodes.Credential(r.Context(), nodeID)
	if err != nil {
		remoteNodeError(w, err)
		return
	}
	job, err := s.remoteClient.GetProvisioningJob(r.Context(), endpoint, credential, jobID)
	if err != nil {
		remoteProvisioningError(w, err)
		return
	}
	// The controller stores no tenant/job mapping of its own (no new
	// cluster-wide reservation or job table is introduced); it instead
	// trusts the target node's own tenant field on the returned job and
	// refuses to hand back a job belonging to a tenant the caller did not
	// just prove Server.Create for. See dispatchRemoteProvisioning's doc
	// comment on why the node stays the sole source of truth.
	if job.TenantID != tenantID {
		notFound(w)
		return
	}
	jsonOut(w, http.StatusOK, job)
}

func (s *Server) remoteProvisioningJobCancelHandler(w http.ResponseWriter, r *http.Request, nodeID, jobID string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		tenantID = tenants.DefaultTenantID
	}
	actor, ok := s.requireTenantProvisionPermission(w, r, tenantID, true)
	if !ok {
		return
	}
	node, err := s.nodes.Get(r.Context(), nodeID)
	if err != nil {
		remoteNodeError(w, err)
		return
	}
	if !node.Enabled {
		errorOut(w, http.StatusBadGateway, string(remote.KindUnreachable), "the remote node is disabled")
		return
	}
	endpoint, credential, err := s.nodes.Credential(r.Context(), nodeID)
	if err != nil {
		remoteNodeError(w, err)
		return
	}
	current, err := s.remoteClient.GetProvisioningJob(r.Context(), endpoint, credential, jobID)
	if err != nil {
		remoteProvisioningError(w, err)
		return
	}
	if current.TenantID != tenantID {
		notFound(w)
		return
	}
	job, err := s.remoteClient.CancelProvisioningJob(r.Context(), endpoint, credential, jobID)
	if err != nil {
		s.recordRemoteProvisioningCancelAudit(r, actor, nodeID, tenantID, jobID, err)
		remoteProvisioningError(w, err)
		return
	}
	s.recordRemoteProvisioningCancelAudit(r, actor, nodeID, tenantID, jobID, nil)
	jsonOut(w, http.StatusOK, job)
}

func (s *Server) recordRemoteProvisioningAudit(r *http.Request, actor auth.User, nodeID, tenantID, runtimeType string, job provisioning.Job, ok bool) {
	metadata := map[string]any{"node_id": nodeID, "tenant_id": tenantID, "runtime_type": runtimeType}
	in := auditInput{action: audit.ServerProvisionStart, resourceType: audit.Server, result: audit.Success, actor: &actor}
	if !ok {
		in.result = audit.Failure
		in.errorCode = "remote_provisioning_failed"
		in.errorSummary = "remote provisioning request failed"
	} else {
		metadata["job_id"] = job.ID
		metadata["template_id"] = job.TemplateID
		in.resourceName = job.ServerName
	}
	in.metadata, _ = json.Marshal(metadata)
	s.recordAudit(r, in)
}

func (s *Server) recordRemoteProvisioningCancelAudit(r *http.Request, actor auth.User, nodeID, tenantID, jobID string, err error) {
	metadata := map[string]any{"node_id": nodeID, "tenant_id": tenantID, "job_id": jobID}
	in := auditInput{action: audit.ServerProvisionCancel, resourceType: audit.Server, result: audit.Success, actor: &actor}
	if err != nil {
		in.result = audit.Failure
		in.errorCode = "remote_provisioning_cancel_failed"
		in.errorSummary = "remote provisioning cancel failed"
	}
	in.metadata, _ = json.Marshal(metadata)
	s.recordAudit(r, in)
}
