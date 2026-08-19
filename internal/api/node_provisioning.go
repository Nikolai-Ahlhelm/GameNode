package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"gamenode/internal/logging"
	"gamenode/internal/provisioning"
)

// remoteControllerActor is the fixed, synthetic provisioning-job actor
// identity used for every job created through the machine-authenticated
// POST /api/v1/node/provisioning path. A Remote Node controller is not a
// local user account on this node - internal/nodes' machine-auth trust
// domain intentionally carries no per-caller user identity (see
// internal/nodes/pairing.go's AuthenticateCaller) - so this fixed label
// takes the place of provisioning.Request.ActorUserID/ActorUsername, which
// provisioning.Service.Start requires to be non-empty. It is never treated
// as, or confused with, a real internal/auth user: nodeProvisioningJobHandler
// authorizes read/cancel on these jobs by machine credential alone, exactly
// like every other /api/v1/node/* route, never by comparing against this
// label.
const remoteControllerActor = "remote-controller"

// nodeProvisioningRequest is the machine-authenticated transport contract for
// POST /api/v1/node/provisioning. It intentionally has exactly the same
// bounded fields as the ordinary browser-facing provisionInput
// (internal/api/provisioning.go) plus TemplateID, which the human-facing
// route instead takes from the URL path - there is no additional field, no
// raw JSON, and no generic map of engine flags a caller could use to reach
// past the node's own provisioning.Service validation.
type nodeProvisioningRequest struct {
	TemplateID       string            `json:"template_id"`
	ServerName       string            `json:"server_name"`
	DirectoryName    string            `json:"directory_name"`
	Variables        map[string]string `json:"variables"`
	RecoverExisting  bool              `json:"recover_existing"`
	TenantID         string            `json:"tenant_id"`
	RuntimeType      string            `json:"runtime_type"`
	Image            string            `json:"image"`
	MemoryLimitBytes int64             `json:"memory_limit_bytes"`
	CPULimitMillis   int               `json:"cpu_limit_millis"`
	PIDsLimit        int64             `json:"pids_limit"`
	TmpfsSizeBytes   int64             `json:"tmpfs_size_bytes"`
}

// nodeProvisioningHandler implements POST /api/v1/node/provisioning: the
// machine-authenticated Remote Node counterpart of the ordinary
// POST /api/v1/templates/{id}/provision route a browser-authenticated
// operator already uses on this same node. It never accepts anything a
// local caller could not already send, and it forwards the request to
// exactly the same provisioning.Service.Start this node's own operators
// use - there is no second provisioning/container-lifecycle implementation
// here. This node remains the sole authority for template/Egg validation,
// the image allowlist, resource limits, its tenant/filesystem sandbox, the
// container installer, job persistence, and final registration through
// servers.Service (see docs/adr/0009-cluster-scheduling-decision-vs-execution.md
// and docs/adr/0008-egg-container-execution.md).
func (s *Server) nodeProvisioningHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.requireMachineAuth(w, r) {
		return
	}
	if s.provisioning == nil {
		errorOut(w, http.StatusServiceUnavailable, "provisioning_unavailable", "provisioning is unavailable")
		return
	}
	var in nodeProvisioningRequest
	if !decodeNodeProvisioningInput(w, r, &in) {
		return
	}
	job, err := s.provisioning.Start(r.Context(), provisioning.Request{
		TemplateID: in.TemplateID, ServerName: in.ServerName, DirectoryName: in.DirectoryName, Values: in.Variables,
		ActorUserID: remoteControllerActor, ActorUsername: remoteControllerActor,
		RecoverExisting: in.RecoverExisting, TenantID: in.TenantID, RuntimeType: in.RuntimeType, Image: in.Image,
		MemoryLimitBytes: in.MemoryLimitBytes, CPULimitMillis: in.CPULimitMillis, PIDsLimit: in.PIDsLimit, TmpfsSizeBytes: in.TmpfsSizeBytes,
	})
	if err != nil {
		s.log.With("module", "Node.Provisioning", "category", logging.CategoryProvisioning).Warn("remote provisioning request rejected", "template_id", in.TemplateID, "tenant_id", in.TenantID, "failure", provisioningFailure(err))
		provisioningError(w, err)
		return
	}
	s.log.With("module", "Node.Provisioning", "category", logging.CategoryProvisioning).Info("remote provisioning job created", "job_id", job.ID, "template_id", job.TemplateID, "tenant_id", job.TenantID, "runtime_type", job.RuntimeType)
	jsonOut(w, http.StatusAccepted, job)
}

// decodeNodeProvisioningInput enforces the same bounded-body, no-unknown-
// fields decoding discipline as decodeProvisionInput (internal/api/provisioning.go)
// for the machine-authenticated transport struct.
func decodeNodeProvisioningInput(w http.ResponseWriter, r *http.Request, value *nodeProvisioningRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		bad(w, "invalid provisioning request")
		return false
	}
	return true
}

// nodeProvisioningJobHandler implements
// GET /api/v1/node/provisioning/{jobID} and
// POST /api/v1/node/provisioning/{jobID}/cancel: the machine-authenticated
// mirror of the browser-facing job status/cancel routes
// (provisioningJobHandler). It calls the same provisioning.Service.Get and
// provisioning.Service.Cancel - no second job-tracking mechanism exists on
// this node for remote-originated jobs.
func (s *Server) nodeProvisioningJobHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/node/provisioning/")
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
	if !s.requireMachineAuth(w, r) {
		return
	}
	if s.provisioning == nil {
		errorOut(w, http.StatusServiceUnavailable, "provisioning_unavailable", "provisioning is unavailable")
		return
	}
	job, err := s.provisioning.Get(r.Context(), parts[0])
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		internal(w)
		return
	}
	if !cancel {
		jsonOut(w, http.StatusOK, job)
		return
	}
	job, err = s.provisioning.Cancel(r.Context(), parts[0], job.ActorUserID)
	if err != nil {
		errorOut(w, http.StatusConflict, "job_not_active", "provisioning job is no longer active")
		return
	}
	jsonOut(w, http.StatusOK, job)
}
