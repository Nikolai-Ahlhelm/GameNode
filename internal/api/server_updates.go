package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/servers"
	"gamenode/internal/serverupdates"
)

// serverUpdateHandler implements GET/POST /api/v1/servers/{id}/update: the
// eligibility/status read and the manual-update start action. Both require
// the server-scoped Server.Update permission - deliberately independent of
// Server.Edit, Server.Start, and Templates.Manage (see spec section 14).
func (s *Server) serverUpdateHandler(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireServerPermission(w, r, "Server.Update", id, false); !ok {
			return
		}
		if s.serverUpdates == nil {
			errorOut(w, http.StatusServiceUnavailable, "server_updates_unavailable", "server updates are unavailable")
			return
		}
		status, err := s.serverUpdates.Status(r.Context(), id)
		if err != nil {
			serverUpdateError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, status)
	case http.MethodPost:
		actor, _, ok := s.requireServerPermission(w, r, "Server.Update", id, true)
		if !ok {
			return
		}
		if s.serverUpdates == nil {
			errorOut(w, http.StatusServiceUnavailable, "server_updates_unavailable", "server updates are unavailable")
			return
		}
		// Starting an update takes no client-supplied input: the App ID,
		// login mode, validate flag, and template provenance all come from
		// the trusted persisted snapshot (see servers.ProvisionedSteamCMD),
		// never from the request body.
		job, err := s.serverUpdates.Start(r.Context(), id, actor.ID, actor.Username)
		if err != nil {
			s.recordServerUpdateStart(r, actor, id, job, err)
			serverUpdateError(w, err)
			return
		}
		s.recordServerUpdateStart(r, actor, id, job, nil)
		jsonOut(w, http.StatusAccepted, job)
	default:
		method(w)
	}
}

func (s *Server) recordServerUpdateStart(r *http.Request, actor auth.User, serverID string, job serverupdates.Job, err error) {
	result := audit.Success
	metadata, _ := json.Marshal(map[string]any{"server_id": serverID, "template_id": job.TemplateID, "template_version": job.TemplateVersion, "app_id": job.AppID, "validate": job.Validate})
	in := auditInput{action: audit.ServerSteamCMDUpdateStart, resourceType: audit.Server, resourceID: &serverID, serverID: &serverID, result: result, metadata: metadata, actor: &actor}
	if err != nil {
		in.result = audit.Failure
		in.errorCode, in.errorSummary = serverUpdateFailure(err)
		in.err = err
	}
	s.recordAudit(r, in)
}

// recordServerUpdateCompletion is the serverupdates.Observer wired in New. It
// records exactly one bounded, sanitized audit event per terminal job
// outcome - never raw SteamCMD output, command lines, or host paths.
func (s *Server) recordServerUpdateCompletion(event serverupdates.Event) {
	metadata, _ := json.Marshal(map[string]any{"server_id": event.Job.ServerID, "template_id": event.Job.TemplateID, "template_version": event.Job.TemplateVersion, "app_id": event.Job.AppID, "validate": event.Job.Validate, "duration_seconds": int64(event.Duration / time.Second)})
	result := audit.Success
	if event.Action == audit.ServerSteamCMDUpdateFail {
		result = audit.Failure
	}
	serverID := event.Job.ServerID
	auditEvent := audit.Event{ActorUserID: &event.Job.ActorUserID, ActorUsername: event.Job.ActorUsername, Action: event.Action, ResourceType: audit.Server, ResourceID: &serverID, ServerID: &serverID, Result: result, Metadata: metadata}
	if result == audit.Failure {
		auditEvent.ErrorCode = "server_update_failed"
		auditEvent.ErrorSummary = "server update failed"
	}
	if err := s.audit.Record(context.Background(), auditEvent); err != nil {
		s.log.With("module", "Audit.ServerUpdates").Error("audit write failed", "error", err.Error(), "action", event.Action)
	}
}

// serverUpdateJobHandler implements GET /api/v1/server-update-jobs/{id} and
// POST /api/v1/server-update-jobs/{id}/cancel, mirroring
// provisioningJobHandler's ownership rule: the job's actor, or an admin.
func (s *Server) serverUpdateJobHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/server-update-jobs/")
	parts := strings.Split(path, "/")
	if parts[0] == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "cancel") {
		notFound(w)
		return
	}
	cancel := len(parts) == 2 && parts[1] == "cancel"
	if cancel && r.Method != http.MethodPost {
		method(w)
		return
	}
	if !cancel && r.Method != http.MethodGet {
		method(w)
		return
	}
	// Server.Update is server-scoped, so the permission check needs the job's
	// server ID; that means the job must be read before the full permission
	// check can run. Still require at least authentication first, so an
	// unauthenticated caller cannot probe job IDs against the database.
	if _, _, ok := s.requireAuth(w, r, false); !ok {
		return
	}
	if s.serverUpdates == nil {
		errorOut(w, http.StatusServiceUnavailable, "server_updates_unavailable", "server updates are unavailable")
		return
	}
	job, err := s.serverUpdates.Get(r.Context(), parts[0])
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		internal(w)
		return
	}
	actor, _, ok := s.requireServerPermission(w, r, "Server.Update", job.ServerID, cancel)
	if !ok {
		return
	}
	if job.ActorUserID != actor.ID && !actor.IsAdmin {
		notFound(w)
		return
	}
	if !cancel {
		jsonOut(w, http.StatusOK, job)
		return
	}
	owner := actor.ID
	if actor.IsAdmin {
		owner = job.ActorUserID
	}
	job, err = s.serverUpdates.Cancel(r.Context(), parts[0], owner)
	if err != nil {
		errorOut(w, http.StatusConflict, "job_not_active", "server update job is no longer active")
		return
	}
	jsonOut(w, http.StatusOK, job)
}

func serverUpdateFailure(err error) (string, string) {
	switch {
	case errors.Is(err, serverupdates.ErrNotEligible):
		return "not_eligible", "server is not eligible for a manual update"
	case errors.Is(err, serverupdates.ErrServerNotStopped):
		return "server_not_stopped", "stop the server before updating"
	case errors.Is(err, serverupdates.ErrTargetConflict), errors.Is(err, servers.ErrUpdateInProgress):
		return "target_conflict", "a server update is already in progress"
	case errors.Is(err, serverupdates.ErrJobNotActive):
		return "job_not_active", "server update job is not active"
	default:
		return "invalid_request", "server update request is invalid"
	}
}

func serverUpdateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		notFound(w)
	case errors.Is(err, serverupdates.ErrNotEligible):
		errorOut(w, http.StatusUnprocessableEntity, "not_eligible", "server is not eligible for a manual update")
	case errors.Is(err, serverupdates.ErrServerNotStopped):
		errorOut(w, http.StatusConflict, "server_not_stopped", "stop the server before updating")
	case errors.Is(err, serverupdates.ErrTargetConflict), errors.Is(err, servers.ErrUpdateInProgress):
		errorOut(w, http.StatusConflict, "target_conflict", "a server update is already in progress")
	case errors.Is(err, serverupdates.ErrJobNotActive):
		errorOut(w, http.StatusConflict, "job_not_active", "server update job is not active")
	default:
		bad(w, "server update request is invalid")
	}
}
