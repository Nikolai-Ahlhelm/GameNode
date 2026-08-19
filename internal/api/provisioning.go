package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/logging"
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	"gamenode/internal/tenants"
)

type provisionInput struct {
	// TenantID selects which tenant the new managed server belongs to. Left
	// empty, it defaults to tenants.DefaultTenantID, matching
	// servers.Server.Validate's and provisioning.Service.Start's identical
	// default. It is only ever resolved into a managed storage path through
	// tenants.TenantServerRoot server-side (see internal/provisioning); a
	// client can never supply a host path here.
	TenantID         string            `json:"tenant_id"`
	ServerName       string            `json:"server_name"`
	DirectoryName    string            `json:"directory_name"`
	Variables        map[string]string `json:"variables"`
	RecoverExisting  bool              `json:"recover_existing"`
	RuntimeType      string            `json:"runtime_type"`
	Image            string            `json:"image"`
	MemoryLimitBytes int64             `json:"memory_limit_bytes"`
	CPULimitMillis   int               `json:"cpu_limit_millis"`
	PIDsLimit        int64             `json:"pids_limit"`
	TmpfsSizeBytes   int64             `json:"tmpfs_size_bytes"`
}

func decodeProvisionInput(w http.ResponseWriter, r *http.Request, value *provisionInput) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		bad(w, "invalid provisioning request")
		return false
	}
	return true
}

func (s *Server) requireProvisionPermission(w http.ResponseWriter, r *http.Request, csrf bool) (auth.User, bool) {
	actor, _, ok := s.requireGlobalPermission(w, r, "Templates.View", csrf)
	if !ok {
		return auth.User{}, false
	}
	allowed, err := s.allowed(r.Context(), actor, "Server.Create", rbac.Scope{Type: "global"})
	if err != nil {
		internal(w)
		return auth.User{}, false
	}
	if !allowed {
		forbidden(w, "permission denied")
		return auth.User{}, false
	}
	return actor, true
}

// startProvisioning requires global Templates.View (can this node install
// this kind of template at all) AND Server.Create effective for the
// requested tenant (global or tenant-scoped; see internal/rbac's evaluator).
// Authorization is checked against the tenant from the request body, so it
// must be decoded before the Server.Create check - unlike every other
// provisioning route, which only ever needed the fixed global check. Tenant
// membership alone never satisfies this: internal/tenants.Membership carries
// no RBAC weight, only a role assignment does.
func (s *Server) startProvisioning(w http.ResponseWriter, r *http.Request, templateID string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	actor, _, ok := s.requireGlobalPermission(w, r, "Templates.View", true)
	if !ok {
		return
	}
	if s.provisioning == nil {
		errorOut(w, http.StatusServiceUnavailable, "provisioning_unavailable", "provisioning is unavailable")
		return
	}
	var input provisionInput
	if !decodeProvisionInput(w, r, &input) {
		return
	}
	tenantID := strings.TrimSpace(input.TenantID)
	if tenantID == "" {
		tenantID = tenants.DefaultTenantID
	}
	allowed, err := s.allowed(r.Context(), actor, "Server.Create", rbac.Scope{Type: "tenant", ID: &tenantID})
	if err != nil {
		internal(w)
		return
	}
	if !allowed {
		forbidden(w, "permission denied")
		return
	}
	job, err := s.provisioning.Start(r.Context(), provisioning.Request{TemplateID: templateID, ServerName: input.ServerName, DirectoryName: input.DirectoryName, Values: input.Variables, ActorUserID: actor.ID, ActorUsername: actor.Username, RecoverExisting: input.RecoverExisting, TenantID: tenantID, RuntimeType: input.RuntimeType, Image: input.Image, MemoryLimitBytes: input.MemoryLimitBytes, CPULimitMillis: input.CPULimitMillis, PIDsLimit: input.PIDsLimit, TmpfsSizeBytes: input.TmpfsSizeBytes})
	if err != nil {
		s.log.With("module", "Provisioning.Start", "category", logging.CategoryProvisioning).Warn("provisioning request rejected", "template_id", templateID, "tenant_id", tenantID, "actor_user_id", actor.ID, "failure", provisioningFailure(err))
		provisioningError(w, err)
		return
	}
	s.log.With("module", "Provisioning.Start", "category", logging.CategoryProvisioning).Info("provisioning job created", "job_id", job.ID, "template_id", job.TemplateID, "tenant_id", job.TenantID, "app_id", job.AppID, "runtime_type", job.RuntimeType, "actor_user_id", actor.ID)
	metadata, _ := json.Marshal(map[string]any{"template_id": job.TemplateID, "job_id": job.ID, "installer_type": job.InstallerType, "app_id": job.AppID, "runtime_type": job.RuntimeType, "tenant_id": job.TenantID})
	s.recordAudit(r, auditInput{action: audit.ServerProvisionStart, resourceType: audit.Server, resourceName: job.ServerName, result: audit.Success, metadata: metadata, actor: &actor})
	jsonOut(w, http.StatusAccepted, job)
}

func provisioningFailure(err error) string {
	switch {
	case errors.Is(err, provisioning.ErrNotProvisionable):
		return "not_provisionable"
	case errors.Is(err, provisioning.ErrTargetConflict):
		return "target_conflict"
	case errors.Is(err, provisioning.ErrRecoveryUnavailable):
		return "recovery_unavailable"
	case errors.Is(err, provisioning.ErrInvalidTenant):
		return "invalid_tenant"
	case errors.Is(err, provisioning.ErrPortPreflightFailed):
		return "port_conflict"
	case errors.Is(err, provisioning.ErrNamePreflightFailed):
		return "name_conflict"
	case errors.Is(err, provisioning.ErrContainerImagePolicy):
		return "container_image_policy_blocked"
	case errors.Is(err, provisioning.ErrContainerImageSelection):
		return "container_image_not_declared"
	case errors.Is(err, provisioning.ErrContainerRuntimeUnavailable):
		return "container_runtime_unavailable"
	case errors.Is(err, sql.ErrNoRows):
		return "template_not_found"
	default:
		return "invalid_request"
	}
}

func (s *Server) templateProvisionability(w http.ResponseWriter, r *http.Request, templateID string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, ok := s.requireProvisionPermission(w, r, false); !ok {
		return
	}
	if s.provisioning == nil {
		errorOut(w, http.StatusServiceUnavailable, "provisioning_unavailable", "provisioning is unavailable")
		return
	}
	result, err := s.provisioning.Check(r.Context(), templateID)
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, result)
}

func (s *Server) provisioningJobHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/provisioning/jobs/")
	parts := strings.Split(path, "/")
	if parts[0] == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "cancel" && parts[1] != "retry-registration") {
		notFound(w)
		return
	}
	cancel := len(parts) == 2 && parts[1] == "cancel"
	retryRegistration := len(parts) == 2 && parts[1] == "retry-registration"
	if (cancel || retryRegistration) && r.Method != http.MethodPost {
		method(w)
		return
	}
	if !cancel && !retryRegistration && r.Method != http.MethodGet {
		method(w)
		return
	}
	actor, ok := s.requireProvisionPermission(w, r, cancel || retryRegistration)
	if !ok {
		return
	}
	if s.provisioning == nil {
		errorOut(w, http.StatusServiceUnavailable, "provisioning_unavailable", "provisioning is unavailable")
		return
	}
	job, err := s.provisioning.Get(r.Context(), parts[0])
	if errors.Is(err, sql.ErrNoRows) || job.ActorUserID != actor.ID && !actor.IsAdmin {
		notFound(w)
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if !cancel && !retryRegistration {
		jsonOut(w, http.StatusOK, job)
		return
	}
	if retryRegistration {
		owner := actor.ID
		if actor.IsAdmin {
			owner = job.ActorUserID
		}
		job, err = s.provisioning.RetryRegistration(r.Context(), parts[0], owner)
		if err != nil {
			metadata, _ := json.Marshal(map[string]any{"template_id": job.TemplateID, "job_id": parts[0]})
			s.recordAudit(r, auditInput{action: audit.ServerProvisionRetry, resourceType: audit.Server, resourceName: job.ServerName, result: audit.Failure, metadata: metadata, errorCode: "registration_retry_unavailable", errorSummary: "server registration retry was unavailable", actor: &actor})
			errorOut(w, http.StatusConflict, "registration_retry_unavailable", "server registration cannot be retried for this provisioning job")
			return
		}
		serverID := job.ServerID
		metadata, _ := json.Marshal(map[string]any{"template_id": job.TemplateID, "job_id": job.ID})
		s.recordAudit(r, auditInput{action: audit.ServerProvisionRetry, resourceType: audit.Server, resourceID: &serverID, resourceName: job.ServerName, serverID: &serverID, result: audit.Success, metadata: metadata, actor: &actor})
		jsonOut(w, http.StatusOK, job)
		return
	}
	owner := actor.ID
	if actor.IsAdmin {
		owner = job.ActorUserID
	}
	job, err = s.provisioning.Cancel(r.Context(), parts[0], owner)
	if err != nil {
		errorOut(w, http.StatusConflict, "job_not_active", "provisioning job is no longer active")
		return
	}
	jsonOut(w, http.StatusOK, job)
}

func (s *Server) recordProvisioningCompletion(event provisioning.Event) {
	metadata, _ := json.Marshal(map[string]any{"template_id": event.Job.TemplateID, "job_id": event.Job.ID, "tenant_id": event.Job.TenantID, "installer_type": event.Job.InstallerType, "runtime_type": event.Job.RuntimeType, "selected_image": event.Job.SelectedImage, "selected_image_digest": event.Job.SelectedImageDigest, "app_id": event.Job.AppID, "duration_seconds": int64(event.Duration / time.Second), "failure_phase": event.Job.FailurePhase, "failure_code": event.Job.FailureCode, "files_may_remain": event.Job.FilesMayRemain})
	var resourceID *string
	var serverID *string
	if event.Job.ServerID != "" {
		id := event.Job.ServerID
		resourceID = &id
		serverID = &id
	}
	result := audit.Success
	if event.Action == audit.ServerProvisionFail {
		result = audit.Failure
	}
	auditEvent := audit.Event{ActorUserID: &event.Job.ActorUserID, ActorUsername: event.Job.ActorUsername, Action: event.Action, ResourceType: audit.Server, ResourceID: resourceID, ResourceName: event.Job.ServerName, ServerID: serverID, Result: result, Metadata: metadata}
	if result == audit.Failure {
		auditEvent.ErrorCode = "provisioning_failed"
		auditEvent.ErrorSummary = "server provisioning failed"
	}
	if err := s.audit.Record(context.Background(), auditEvent); err != nil {
		s.log.With("module", "Audit.Provisioning").Error("audit write failed", "error", err.Error(), "action", event.Action)
	}
}

func provisioningError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		notFound(w)
	case errors.Is(err, provisioning.ErrNotProvisionable):
		errorOut(w, http.StatusUnprocessableEntity, "not_provisionable", "template is not provisionable on this host")
	case errors.Is(err, provisioning.ErrTargetConflict):
		errorOut(w, http.StatusConflict, "target_conflict", "server target is already populated or in use")
	case errors.Is(err, provisioning.ErrRecoveryUnavailable):
		errorOut(w, http.StatusConflict, "recovery_unavailable", "the installed server cannot be safely recovered")
	case errors.Is(err, provisioning.ErrInvalidTenant):
		errorOut(w, http.StatusBadRequest, "invalid_tenant", "tenant does not exist")
	case errors.Is(err, provisioning.ErrPortPreflightFailed):
		// err.Error() is safe to surface: it is built from
		// ErrPortPreflightFailed's fixed text plus internal/ports' collision
		// message, which names only the conflicting protocol/port - never
		// host internals, PIDs, or filesystem paths.
		errorOut(w, http.StatusConflict, "port_conflict", err.Error())
	case errors.Is(err, provisioning.ErrNamePreflightFailed):
		// err.Error() is safe to surface for the same reason: it is
		// ErrNamePreflightFailed's fixed text plus internal/servers'
		// ErrDuplicateName, never raw SQL/driver text.
		errorOut(w, http.StatusConflict, "name_conflict", err.Error())
	case errors.Is(err, provisioning.ErrContainerImagePolicy):
		errorOut(w, http.StatusUnprocessableEntity, "container_image_policy_blocked", "the selected Egg image is blocked by the node image policy")
	case errors.Is(err, provisioning.ErrContainerImageSelection):
		errorOut(w, http.StatusUnprocessableEntity, "container_image_not_declared", "the selected image is not declared by the Egg")
	case errors.Is(err, provisioning.ErrContainerRuntimeUnavailable):
		errorOut(w, http.StatusServiceUnavailable, "container_runtime_unavailable", "the container Egg runtime is unavailable")
	default:
		errorOut(w, http.StatusBadRequest, "invalid_provision_request", "provisioning request is invalid")
	}
}
