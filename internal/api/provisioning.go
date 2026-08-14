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
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
)

type provisionInput struct {
	ServerName      string            `json:"server_name"`
	DirectoryName   string            `json:"directory_name"`
	Variables       map[string]string `json:"variables"`
	RecoverExisting bool              `json:"recover_existing"`
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

func (s *Server) startProvisioning(w http.ResponseWriter, r *http.Request, templateID string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	actor, ok := s.requireProvisionPermission(w, r, true)
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
	job, err := s.provisioning.Start(r.Context(), provisioning.Request{TemplateID: templateID, ServerName: input.ServerName, DirectoryName: input.DirectoryName, Values: input.Variables, ActorUserID: actor.ID, ActorUsername: actor.Username, RecoverExisting: input.RecoverExisting})
	if err != nil {
		s.log.With("module", "Provisioning.Start").Warn("provisioning request rejected", "template_id", templateID, "actor_user_id", actor.ID, "failure", provisioningFailure(err))
		provisioningError(w, err)
		return
	}
	s.log.With("module", "Provisioning.Start").Info("provisioning job created", "job_id", job.ID, "template_id", job.TemplateID, "app_id", job.AppID, "actor_user_id", actor.ID)
	metadata, _ := json.Marshal(map[string]any{"template_id": job.TemplateID, "job_id": job.ID, "installer_type": job.InstallerType, "app_id": job.AppID})
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
	metadata, _ := json.Marshal(map[string]any{"template_id": event.Job.TemplateID, "job_id": event.Job.ID, "installer_type": event.Job.InstallerType, "app_id": event.Job.AppID, "duration_seconds": int64(event.Duration / time.Second), "failure_phase": event.Job.FailurePhase, "failure_code": event.Job.FailureCode, "files_may_remain": event.Job.FilesMayRemain})
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
	default:
		errorOut(w, http.StatusBadRequest, "invalid_provision_request", "provisioning request is invalid")
	}
}
