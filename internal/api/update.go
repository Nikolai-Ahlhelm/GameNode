package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/nodeidentity"
	"gamenode/internal/nodes"
	"gamenode/internal/remote"
	"gamenode/internal/selfupdate"
)

// selfUpdater is the narrow surface of internal/selfupdate the transport
// layer needs. *selfupdate.Service satisfies it.
type selfUpdater interface {
	Status(ctx context.Context) selfupdate.Status
	Summary() selfupdate.Status
	Check(ctx context.Context) (selfupdate.Status, error)
	Prepare(ctx context.Context, version string) error
	Apply(ctx context.Context, version string, acknowledge bool, actor selfupdate.Actor) error
	Cancel() error
}

// This file implements the three faces of GameNode self-update
// (docs/adr/0013-self-update.md):
//
//   - /api/v1/system/update*          browser + RBAC + CSRF: this installation.
//   - /api/v1/node/update*            machine-authenticated: an enrolled
//     controller asking THIS node to update itself.
//   - /api/v1/remote-nodes/{id}/update*  browser + RBAC + CSRF: this
//     installation acting as controller for an enrolled node.
//
// None of them accepts a URL, binary, checksum, or path. The only
// caller-supplied value is the version string the administrator saw in a
// status response, and the receiving updater re-validates it against the
// release it fetched itself.

const maxUpdateRequestBytes = 4 << 10

type updateRequest struct {
	Version             string `json:"version"`
	AcknowledgeWarnings bool   `json:"acknowledge_warnings"`
}

func (s *Server) updaterUnavailable(w http.ResponseWriter) bool {
	if s.updater != nil {
		return false
	}
	errorOut(w, http.StatusServiceUnavailable, "update_unavailable", "self-update is not available in this build")
	return true
}

func decodeUpdateRequest(w http.ResponseWriter, r *http.Request, required bool) (updateRequest, bool) {
	var in updateRequest
	if !required && (r.Body == nil || r.ContentLength == 0) {
		return in, true
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUpdateRequestBytes)
	if !decode(w, r, &in) {
		return in, false
	}
	if required && !selfupdate.ValidVersionText(in.Version) {
		bad(w, "a valid release version is required")
		return in, false
	}
	return in, true
}

// updateStatusForCode maps a selfupdate error code to an HTTP status.
func updateStatusForCode(code string) int {
	switch code {
	case selfupdate.CodeBusy, selfupdate.CodeReleaseUnknown, selfupdate.CodeNotReady,
		selfupdate.CodePreflightBlocked, selfupdate.CodeAcknowledge, selfupdate.CodeUnsupported:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// writeUpdateError renders a selfupdate.Error. The body keeps the standard
// {"error":{code,message}} envelope and adds the safety-check results, so the
// UI can show exactly what blocked the operation.
func writeUpdateError(w http.ResponseWriter, err error) (code, message string) {
	var updateErr *selfupdate.Error
	if !errors.As(err, &updateErr) {
		internal(w)
		return "internal_error", "an internal error occurred"
	}
	body := map[string]any{"error": map[string]string{"code": updateErr.Code, "message": updateErr.Message}}
	if len(updateErr.Checks) > 0 {
		body["checks"] = updateErr.Checks
	}
	jsonOut(w, updateStatusForCode(updateErr.Code), body)
	return updateErr.Code, updateErr.Message
}

// --- this installation ------------------------------------------------------

// systemUpdateHandler routes /api/v1/system/update[/{check|prepare|apply|cancel}].
func (s *Server) systemUpdateHandler(w http.ResponseWriter, r *http.Request) {
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/system/update"), "/")
	switch action {
	case "":
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		if _, _, ok := s.requireGlobalPermission(w, r, "Update.View", false); !ok {
			return
		}
		if s.updaterUnavailable(w) {
			return
		}
		if r.URL.Query().Get("summary") == "1" {
			// Cheap form for periodic pollers: no safety checks are evaluated.
			jsonOut(w, http.StatusOK, s.updater.Summary())
			return
		}
		jsonOut(w, http.StatusOK, s.updater.Status(r.Context()))
	case "check":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		if _, _, ok := s.requireGlobalPermission(w, r, "Update.View", true); !ok {
			return
		}
		if s.updaterUnavailable(w) {
			return
		}
		extendWriteDeadline(w, time.Minute)
		status, err := s.updater.Check(r.Context())
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, status)
	case "prepare", "apply", "cancel":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		actor, _, ok := s.requireGlobalPermission(w, r, "Update.Manage", true)
		if !ok {
			return
		}
		if s.updaterUnavailable(w) {
			return
		}
		s.localUpdateAction(w, r, action, actor)
	default:
		notFound(w)
	}
}

// extendWriteDeadline lifts the server-wide response deadline for the few
// handlers that legitimately run longer than it (a release lookup, a database
// backup before an install, or waiting on a remote node). Without it a
// successful install could complete while its own response timed out.
func extendWriteDeadline(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
}

func (s *Server) localUpdateAction(w http.ResponseWriter, r *http.Request, action string, actor auth.User) {
	extendWriteDeadline(w, 3*time.Minute)
	in, ok := decodeUpdateRequest(w, r, action != "cancel")
	if !ok {
		return
	}
	switch action {
	case "prepare":
		if err := s.updater.Prepare(r.Context(), in.Version); err != nil {
			writeUpdateError(w, err)
			return
		}
		jsonOut(w, http.StatusAccepted, s.updater.Status(r.Context()))
	case "cancel":
		if err := s.updater.Cancel(); err != nil {
			writeUpdateError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, s.updater.Status(r.Context()))
	case "apply":
		before := s.updater.Status(r.Context()).CurrentVersion
		err := s.updater.Apply(r.Context(), in.Version, in.AcknowledgeWarnings, selfupdate.Actor{ID: actor.ID, Username: actor.Username})
		s.recordUpdateApplyAudit(r, &actor, "administrator", before, in, err)
		if err != nil {
			writeUpdateError(w, err)
			return
		}
		jsonOut(w, http.StatusAccepted, s.updater.Status(r.Context()))
	}
}

// recordUpdateApplyAudit records the synchronous outcome of one install
// request. Metadata is limited to version strings and a boolean; failure
// codes are the controlled selfupdate.Code* values.
func (s *Server) recordUpdateApplyAudit(r *http.Request, actor *auth.User, initiator, from string, in updateRequest, err error) {
	metadata, _ := json.Marshal(map[string]any{"from_version": selfupdate.CanonicalVersion(from), "to_version": selfupdate.CanonicalVersion(boundedVersion(in.Version)), "initiator": initiator, "warnings_acknowledged": in.AcknowledgeWarnings})
	input := auditInput{action: audit.SystemUpdateApply, resourceType: audit.System, resourceName: "GameNode", result: audit.Success, actor: actor, metadata: metadata}
	if err != nil {
		var updateErr *selfupdate.Error
		if errors.As(err, &updateErr) {
			input.errorCode, input.errorSummary = updateErr.Code, updateErr.Message
		} else {
			input.errorCode, input.errorSummary = "operation_failed", "operation failed"
		}
		input.result, input.err = audit.Failure, err
	}
	s.recordAudit(r, input)
}

func boundedVersion(version string) string {
	if len(version) > 64 {
		return version[:64]
	}
	return version
}

// RecordUpdateOutcome writes the post-restart audit event for an update that
// completed or was rolled back. It is called once by cmd/gamenode.
func (s *Server) RecordUpdateOutcome(outcome selfupdate.Outcome) {
	event := audit.Event{ResourceType: audit.System, ResourceName: "GameNode", Result: audit.Success, RemoteIP: ""}
	metadata := map[string]any{"from_version": outcome.From, "to_version": outcome.To}
	switch outcome.Result {
	case "completed":
		event.Action = audit.SystemUpdateComplete
	case "rolled_back":
		event.Action = audit.SystemUpdateRollback
		event.Result = audit.Failure
		event.ErrorCode = "update_rolled_back"
		event.ErrorSummary = "the updated version did not become healthy and the previous version was restored"
		metadata["reason"] = outcome.Reason
	default:
		return
	}
	if outcome.ActorID != "" {
		id := outcome.ActorID
		event.ActorUserID = &id
		event.ActorUsername = outcome.ActorUsername
	}
	event.Metadata, _ = json.Marshal(metadata)
	if err := s.audit.Record(context.Background(), event); err != nil {
		s.log.With("module", "Audit.Record").Error("audit write failed", "error", err.Error(), "action", event.Action)
	}
}

// --- machine-authenticated: an enrolled controller asking THIS node ----------

// nodeUpdateHandler routes /api/v1/node/update[/{check|prepare|apply|cancel}].
// Like every /api/v1/node/* endpoint it authenticates a machine credential and
// never consults RBAC or CSRF (see requireMachineAuth).
func (s *Server) nodeUpdateHandler(w http.ResponseWriter, r *http.Request) {
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/node/update"), "/")
	switch action {
	case "", "check", "prepare", "apply", "cancel":
	default:
		notFound(w)
		return
	}
	wantMethod := http.MethodPost
	if action == "" {
		wantMethod = http.MethodGet
	}
	if r.Method != wantMethod {
		method(w)
		return
	}
	if !s.requireMachineAuth(w, r) {
		return
	}
	if s.updaterUnavailable(w) {
		return
	}
	extendWriteDeadline(w, 3*time.Minute)
	switch action {
	case "":
		jsonOut(w, http.StatusOK, s.updater.Status(r.Context()))
	case "check":
		status, err := s.updater.Check(r.Context())
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, status)
	case "prepare", "apply", "cancel":
		in, ok := decodeUpdateRequest(w, r, action != "cancel")
		if !ok {
			return
		}
		switch action {
		case "prepare":
			if err := s.updater.Prepare(r.Context(), in.Version); err != nil {
				writeUpdateError(w, err)
				return
			}
			jsonOut(w, http.StatusAccepted, s.updater.Status(r.Context()))
		case "cancel":
			if err := s.updater.Cancel(); err != nil {
				writeUpdateError(w, err)
				return
			}
			jsonOut(w, http.StatusOK, s.updater.Status(r.Context()))
		case "apply":
			before := s.updater.Status(r.Context()).CurrentVersion
			err := s.updater.Apply(r.Context(), in.Version, in.AcknowledgeWarnings, selfupdate.Actor{Username: "controller"})
			s.recordUpdateApplyAudit(r, nil, "controller", before, in, err)
			if err != nil {
				writeUpdateError(w, err)
				return
			}
			jsonOut(w, http.StatusAccepted, s.updater.Status(r.Context()))
		}
	}
}

// --- controller side: acting on an enrolled node ----------------------------

// remoteUpdateClient is the subset of the typed remote client used here.
type remoteUpdateClient interface {
	GetUpdateStatus(ctx context.Context, endpoint, credential string) (selfupdate.Status, error)
	CheckUpdate(ctx context.Context, endpoint, credential string) (selfupdate.Status, error)
	PrepareUpdate(ctx context.Context, endpoint, credential, version string) (selfupdate.Status, error)
	ApplyUpdate(ctx context.Context, endpoint, credential, version string, acknowledgeWarnings bool) (selfupdate.Status, error)
	CancelUpdate(ctx context.Context, endpoint, credential string) (selfupdate.Status, error)
}

const remoteUpdateTimeout = 85 * time.Second

// remoteNodeUpdateHandler routes /api/v1/remote-nodes/{id}/update[/{action}].
//
// Reading needs Node.View and Update.View; changing anything needs
// Node.Manage and Update.Manage. Neither implies the other: administering the
// registry does not authorize replacing a node's software, and vice versa.
func (s *Server) remoteNodeUpdateHandler(w http.ResponseWriter, r *http.Request, id string, rest []string) {
	if len(rest) > 1 {
		notFound(w)
		return
	}
	action := ""
	if len(rest) == 1 {
		action = rest[0]
	}
	var viewOnly bool
	switch action {
	case "":
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		viewOnly = true
	case "check":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		viewOnly = true
	case "prepare", "apply", "cancel":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
	default:
		notFound(w)
		return
	}
	csrf := r.Method != http.MethodGet
	var actor auth.User
	var ok bool
	if viewOnly {
		if actor, _, ok = s.requireGlobalPermission(w, r, "Node.View", csrf); !ok {
			return
		}
		if _, _, ok = s.requireGlobalPermission(w, r, "Update.View", csrf); !ok {
			return
		}
	} else {
		if actor, _, ok = s.requireGlobalPermission(w, r, "Node.Manage", csrf); !ok {
			return
		}
		if _, _, ok = s.requireGlobalPermission(w, r, "Update.Manage", csrf); !ok {
			return
		}
	}
	node, err := s.nodes.Get(r.Context(), id)
	if err != nil {
		remoteNodeError(w, err)
		return
	}
	client := s.remoteClient
	if !nodeSupportsUpdate(node) {
		if action == "" {
			jsonOut(w, http.StatusOK, map[string]any{"supported": false})
			return
		}
		errorOut(w, http.StatusConflict, "remote_update_unsupported", "this node's GameNode version predates remote updates; update it manually once")
		return
	}
	if !node.Enabled {
		errorOut(w, http.StatusConflict, "remote_node_disabled", "the remote node is disabled")
		return
	}
	in, ok := decodeUpdateRequest(w, r, action == "prepare" || action == "apply")
	if !ok {
		return
	}
	extendWriteDeadline(w, remoteUpdateTimeout+15*time.Second)
	ctx, cancel := context.WithTimeout(r.Context(), remoteUpdateTimeout)
	defer cancel()
	var status selfupdate.Status
	switch action {
	case "":
		status, err = client.GetUpdateStatus(ctx, node.Endpoint, node.Credential)
	case "check":
		status, err = client.CheckUpdate(ctx, node.Endpoint, node.Credential)
	case "prepare":
		status, err = client.PrepareUpdate(ctx, node.Endpoint, node.Credential, in.Version)
	case "cancel":
		status, err = client.CancelUpdate(ctx, node.Endpoint, node.Credential)
	case "apply":
		status, err = client.ApplyUpdate(ctx, node.Endpoint, node.Credential, in.Version, in.AcknowledgeWarnings)
		s.recordRemoteUpdateAudit(r, actor, node, in, err)
	}
	if err != nil {
		writeRemoteUpdateError(w, err)
		return
	}
	code := http.StatusOK
	if action == "prepare" || action == "apply" {
		code = http.StatusAccepted
	}
	jsonOut(w, code, map[string]any{"supported": true, "update": clampRemoteUpdateStatus(status)})
}

func nodeSupportsUpdate(node nodes.RemoteNode) bool {
	for _, capability := range node.Capabilities {
		if capability == string(nodeidentity.CapabilitySelfUpdate) {
			return true
		}
	}
	return false
}

func (s *Server) recordRemoteUpdateAudit(r *http.Request, actor auth.User, node nodes.RemoteNode, in updateRequest, err error) {
	metadata, _ := json.Marshal(map[string]any{"node_id": node.NodeID, "from_version": selfupdate.CanonicalVersion(boundedVersion(node.GameNodeVersion)), "to_version": selfupdate.CanonicalVersion(boundedVersion(in.Version)), "warnings_acknowledged": in.AcknowledgeWarnings})
	nodeID := node.ID
	input := auditInput{action: audit.NodeSoftwareUpdate, resourceType: audit.Node, resourceID: &nodeID, resourceName: node.DisplayName, result: audit.Success, actor: &actor, metadata: metadata}
	if err != nil {
		input.result, input.err = audit.Failure, err
		input.errorCode, input.errorSummary = remoteUpdateFailure(err)
	}
	s.recordAudit(r, input)
}

// knownUpdateCodes is the whitelist of node-reported error codes a controller
// will repeat. Anything else is reported generically: a remote node's own
// message text is never echoed (see AGENTS.md's remote error rule).
var knownUpdateCodes = map[string]string{
	selfupdate.CodeBusy:             "the remote node is busy with another update operation",
	selfupdate.CodeReleaseUnknown:   "the remote node does not know that release; check for updates on the node again",
	selfupdate.CodeNotReady:         "the remote node has no verified update staged; download it first",
	selfupdate.CodePreflightBlocked: "the remote node's safety checks are blocking this update",
	selfupdate.CodeAcknowledge:      "the update has warnings that must be acknowledged",
	selfupdate.CodeUnsupported:      "this build cannot update itself",
	selfupdate.CodeBackupFailed:     "the remote node could not back up its database; nothing was installed",
	selfupdate.CodeInstallFailed:    "the remote node could not install the new executable",
	selfupdate.CodeMarkerFailed:     "the remote node could not write its rollback record; nothing was installed",
	selfupdate.CodeStagedTampered:   "the staged download changed after verification and was discarded",
}

func remoteUpdateFailure(err error) (string, string) {
	var updateErr *remote.UpdateError
	if errors.As(err, &updateErr) {
		if message, ok := knownUpdateCodes[updateErr.Code]; ok {
			return updateErr.Code, message
		}
		return "node_update_failed", "the remote node rejected the update request"
	}
	var remoteErr *remote.Error
	if errors.As(err, &remoteErr) {
		return string(remoteErr.Kind), remoteErrorMessage(remoteErr.Kind)
	}
	return "operation_failed", "operation failed"
}

func writeRemoteUpdateError(w http.ResponseWriter, err error) {
	var updateErr *remote.UpdateError
	if errors.As(err, &updateErr) {
		code, message := remoteUpdateFailure(err)
		status := http.StatusBadGateway
		if _, known := knownUpdateCodes[updateErr.Code]; known && updateErr.StatusCode == http.StatusConflict {
			status = http.StatusConflict
		}
		body := map[string]any{"error": map[string]string{"code": code, "message": message}}
		if checks := clampChecks(updateErr.Checks); len(checks) > 0 {
			body["checks"] = checks
		}
		jsonOut(w, status, body)
		return
	}
	remoteNodeError(w, err)
}

// clampChecks bounds and normalizes safety-check results reported by a remote
// node before they are relayed to a browser: at most 32 entries, bounded text,
// and any status other than pass/warn treated as a block.
func clampChecks(checks []selfupdate.Check) []selfupdate.Check {
	if len(checks) > 32 {
		checks = checks[:32]
	}
	out := make([]selfupdate.Check, 0, len(checks))
	for _, check := range checks {
		switch check.Status {
		case selfupdate.CheckPass, selfupdate.CheckWarn, selfupdate.CheckBlock:
		default:
			check.Status = selfupdate.CheckBlock
		}
		check.ID, check.Label, check.Message = clampText(check.ID, 64), clampText(check.Label, 120), clampText(check.Message, 400)
		out = append(out, check)
	}
	return out
}

func clampText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) > limit {
		value = strings.ToValidUTF8(value[:limit], "")
	}
	return value
}

func clampRemoteUpdateStatus(status selfupdate.Status) selfupdate.Status {
	status.Checks = clampChecks(status.Checks)
	if status.Available != nil {
		available := *status.Available
		available.Notes = clampText(available.Notes, selfupdate.MaxNotesBytes)
		available.Name = clampText(available.Name, 200)
		if len(available.Assets) > 32 {
			available.Assets = available.Assets[:32]
		}
		status.Available = &available
	}
	return status
}
