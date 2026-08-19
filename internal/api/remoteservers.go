package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/console"
	"gamenode/internal/filesystem"
	"gamenode/internal/nodes"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
)

// This file implements the controller-facing surface for v0.5B Remote
// Server Management and v0.5C Remote Operational Hardening: ordinary
// browser-authenticated, RBAC- and CSRF-protected endpoints under
// /api/v1/remote-nodes/{id}/servers/... The controller NEVER touches a
// remote node's database, process table, or container runtime directly -
// every operation here is a single, bounded, typed call through
// s.remoteClient (internal/remote.Client) against the Node API implemented
// in internal/api/node_servers.go, which forwards it to that node's own
// servers.Service/filesystem sandbox (see
// docs/adr/0010-remote-server-lifecycle-forwarding.md). Remote server data
// held here is always a bounded transport/cache projection, never an
// authoritative lifecycle source - "node offline" never means "server
// stopped" (see AGENTS.md item 21/35, carried over from v0.5A).

// remoteServersRouter dispatches every path under
// /api/v1/remote-nodes/{nodeID}/servers/... "rest" is the path with the
// leading "{nodeID}/servers/" already stripped.
func (s *Server) remoteServersRouter(w http.ResponseWriter, r *http.Request, nodeID string, rest []string) {
	if len(rest) == 0 || rest[0] == "" {
		s.remoteServersHandler(w, r, nodeID)
		return
	}
	serverID := rest[0]
	if len(rest) == 1 {
		s.remoteServerHandler(w, r, nodeID, serverID)
		return
	}
	switch rest[1] {
	case "start", "stop", "restart", "kill":
		if len(rest) != 2 {
			notFound(w)
			return
		}
		s.remoteServerLifecycleHandler(w, r, nodeID, serverID, rest[1])
	case "console":
		if len(rest) != 2 && len(rest) != 3 {
			notFound(w)
			return
		}
		if len(rest) == 3 && rest[2] != "ws" {
			notFound(w)
			return
		}
		if len(rest) == 3 {
			s.remoteServerConsoleRelayHandler(w, r, nodeID, serverID)
			return
		}
		s.remoteServerConsoleHandler(w, r, nodeID, serverID)
	case "monitoring":
		if len(rest) != 2 {
			notFound(w)
			return
		}
		s.remoteServerMonitoringHandler(w, r, nodeID, serverID)
	case "files":
		s.remoteServerFilesHandler(w, r, nodeID, serverID, rest[2:])
	default:
		notFound(w)
	}
}

// requireEnabledRemoteNode resolves the enrolled remote node, rejects a
// disabled entry (409 node_disabled - never contacted), and rejects a node
// that never advertised the required capability (501
// node_capability_unsupported) instead of attempting a call an old node
// cannot serve. See AGENTS.md item 32/34 and the protocol/compatibility
// rules in docs/adr/0010-remote-server-lifecycle-forwarding.md.
func (s *Server) requireEnabledRemoteNode(w http.ResponseWriter, r *http.Request, nodeID, capability string) (nodes.RemoteNode, bool) {
	n, err := s.nodes.Get(r.Context(), nodeID)
	if err != nil {
		remoteNodeError(w, err)
		return nodes.RemoteNode{}, false
	}
	if !n.Enabled {
		errorOut(w, http.StatusConflict, "node_disabled", "remote node is disabled")
		return nodes.RemoteNode{}, false
	}
	if capability != "" && !containsCapability(n.Capabilities, capability) {
		errorOut(w, http.StatusNotImplemented, "node_capability_unsupported", "remote node does not support this operation")
		return nodes.RemoteNode{}, false
	}
	return n, true
}

func (s *Server) allowedRemoteScope(ctx context.Context, u auth.User, permission, tenantID string) (bool, error) {
	global, err := s.allowed(ctx, u, permission, rbac.Scope{Type: "global"})
	if err != nil || global {
		return global, err
	}
	if tenantID == "" {
		return false, nil
	}
	return s.allowed(ctx, u, permission, rbac.Scope{Type: "tenant", ID: &tenantID})
}

// authorizeRemoteServer authenticates the caller, fetches the target
// server's AUTHORITATIVE record from the remote node itself (never trusting
// a locally cached tenant id), and checks permission at global or - only
// against that authoritative tenant id - tenant scope. See AGENTS.md's
// tenant-isolation rule: "a node cannot simply claim a server belongs to
// tenant X" - this is why every action re-fetches from the node rather than
// trusting anything the browser or a prior response said.
func (s *Server) authorizeRemoteServer(w http.ResponseWriter, r *http.Request, n nodes.RemoteNode, serverID, permission string, csrfRequired bool) (auth.User, remote.ServerSummary, bool) {
	u, _, ok := s.requireAuth(w, r, csrfRequired)
	if !ok {
		return auth.User{}, remote.ServerSummary{}, false
	}
	summary, err := s.remoteClient.GetServer(r.Context(), n.Endpoint, n.Credential, serverID)
	if err != nil {
		remoteServerError(w, err)
		return auth.User{}, remote.ServerSummary{}, false
	}
	allowed, err := s.allowedRemoteScope(r.Context(), u, permission, summary.TenantID)
	if err != nil {
		internal(w)
		return auth.User{}, remote.ServerSummary{}, false
	}
	if !allowed {
		forbidden(w, "permission denied")
		return auth.User{}, remote.ServerSummary{}, false
	}
	return u, summary, true
}

func remoteServerError(w http.ResponseWriter, err error) {
	var remoteErr *remote.Error
	if errors.As(err, &remoteErr) {
		switch remoteErr.Kind {
		case remote.KindResourceNotFound:
			notFound(w)
		case remote.KindResourceConflict:
			errorOut(w, http.StatusConflict, "invalid_state", "remote server state does not allow this operation")
		default:
			errorOut(w, http.StatusBadGateway, string(remoteErr.Kind), remoteErrorMessage(remoteErr.Kind))
		}
		return
	}
	if errors.Is(err, nodes.ErrNotFound) {
		notFound(w)
		return
	}
	bad(w, "invalid remote server request")
}

func remoteServerAuditFailure(err error) (string, string) {
	var remoteErr *remote.Error
	if errors.As(err, &remoteErr) {
		return string(remoteErr.Kind), remoteErrorMessage(remoteErr.Kind)
	}
	if errors.Is(err, nodes.ErrNotFound) {
		return "node_not_found", "remote node not found"
	}
	return "operation_failed", "operation failed"
}

// recordRemoteServerAudit records every remote-server mutation/lifecycle
// action under a distinct remote_server resource type (never the local
// "server" type - see internal/audit's doc comments), always tagged with
// the node id in metadata. It is deliberately never called for read-only
// list/get/console-view/monitoring-view operations (see AGENTS.md item 26).
func (s *Server) recordRemoteServerAudit(r *http.Request, actor auth.User, action, result, nodeID, serverID, name string, extra map[string]any, err error) {
	metadata := map[string]any{"node_id": nodeID}
	for k, v := range extra {
		metadata[k] = v
	}
	resourceID := serverID
	in := auditInput{action: action, resourceType: audit.RemoteServer, resourceID: &resourceID, resourceName: name, result: result, actor: &actor}
	if result == audit.Success {
		in.metadata, _ = json.Marshal(metadata)
	} else if err != nil {
		in.errorCode, in.errorSummary = remoteServerAuditFailure(err)
	}
	s.recordAudit(r, in)
}

type publicRemoteServer struct {
	Server     remote.ServerSummary `json:"server"`
	NodeID     string               `json:"node_id"`
	TenantName string               `json:"tenant_name,omitempty"`
}

func (s *Server) toPublicRemoteServer(ctx context.Context, nodeID string, summary remote.ServerSummary) publicRemoteServer {
	return publicRemoteServer{Server: summary, NodeID: nodeID, TenantName: s.tenantName(ctx, summary.TenantID)}
}

// remoteServersHandler implements GET (bounded, tenant-filtered list) and
// POST (create) /api/v1/remote-nodes/{nodeID}/servers.
func (s *Server) remoteServersHandler(w http.ResponseWriter, r *http.Request, nodeID string) {
	switch r.Method {
	case http.MethodGet:
		u, _, ok := s.requireAuth(w, r, false)
		if !ok {
			return
		}
		n, ok := s.requireEnabledRemoteNode(w, r, nodeID, "remote_server_management")
		if !ok {
			return
		}
		global, err := s.allowed(r.Context(), u, "RemoteServer.View", rbac.Scope{Type: "global"})
		if err != nil {
			internal(w)
			return
		}
		list, err := s.remoteClient.ListServers(r.Context(), n.Endpoint, n.Credential)
		if err != nil {
			remoteServerError(w, err)
			return
		}
		out := make([]publicRemoteServer, 0, len(list))
		for _, summary := range list {
			if !global {
				allowedTenant, err := s.allowed(r.Context(), u, "RemoteServer.View", rbac.Scope{Type: "tenant", ID: &summary.TenantID})
				if err != nil {
					internal(w)
					return
				}
				if !allowedTenant {
					continue
				}
			}
			out = append(out, s.toPublicRemoteServer(r.Context(), nodeID, summary))
		}
		jsonOut(w, http.StatusOK, map[string]any{"remote_servers": out})
	case http.MethodPost:
		// Deliberately global-only, mirroring Server.Create's own
		// global-only handler check (see internal/api/servers.go): a
		// create request carries an operator-supplied working directory
		// and executable path on the TARGET node, so a tenant-scoped grant
		// must never reach this path.
		actor, _, ok := s.requirePermission(w, r, "RemoteServer.Manage", rbac.Scope{Type: "global"}, true)
		if !ok {
			return
		}
		n, ok := s.requireEnabledRemoteNode(w, r, nodeID, "remote_server_management")
		if !ok {
			return
		}
		var in remote.CreateServerInput
		if !decode(w, r, &in) {
			return
		}
		summary, err := s.remoteClient.CreateServer(r.Context(), n.Endpoint, n.Credential, in)
		if err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteServerCreate, audit.Failure, nodeID, "", strings.TrimSpace(in.Name), nil, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteServerCreate, audit.Success, nodeID, summary.ID, summary.Name, nil, nil)
		jsonOut(w, http.StatusCreated, map[string]any{"remote_server": s.toPublicRemoteServer(r.Context(), nodeID, summary)})
	default:
		method(w)
	}
}

// remoteServerHandler implements GET/PATCH/DELETE /api/v1/remote-nodes/{nodeID}/servers/{serverID}.
func (s *Server) remoteServerHandler(w http.ResponseWriter, r *http.Request, nodeID, serverID string) {
	permission, csrfRequired := "RemoteServer.View", false
	switch r.Method {
	case http.MethodPatch, http.MethodDelete:
		permission, csrfRequired = "RemoteServer.Manage", true
	case http.MethodGet:
	default:
		method(w)
		return
	}
	n, ok := s.requireEnabledRemoteNode(w, r, nodeID, "remote_server_management")
	if !ok {
		return
	}
	actor, summary, ok := s.authorizeRemoteServer(w, r, n, serverID, permission, csrfRequired)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		jsonOut(w, http.StatusOK, map[string]any{"remote_server": s.toPublicRemoteServer(r.Context(), nodeID, summary)})
	case http.MethodPatch:
		var in remote.UpdateServerInput
		if !decode(w, r, &in) {
			return
		}
		updated, err := s.remoteClient.UpdateServer(r.Context(), n.Endpoint, n.Credential, serverID, in)
		if err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteServerUpdate, audit.Failure, nodeID, serverID, summary.Name, nil, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteServerUpdate, audit.Success, nodeID, serverID, updated.Name, nil, nil)
		jsonOut(w, http.StatusOK, map[string]any{"remote_server": s.toPublicRemoteServer(r.Context(), nodeID, updated)})
	case http.MethodDelete:
		if err := s.remoteClient.DeleteServer(r.Context(), n.Endpoint, n.Credential, serverID); err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteServerDelete, audit.Failure, nodeID, serverID, summary.Name, nil, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteServerDelete, audit.Success, nodeID, serverID, summary.Name, nil, nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

var remoteLifecycleAction = map[string]string{
	"start": audit.RemoteServerStart, "stop": audit.RemoteServerStop,
	"restart": audit.RemoteServerRestart, "kill": audit.RemoteServerKill,
}
var remoteLifecyclePermission = map[string]string{
	"start": "RemoteServer.Manage", "stop": "RemoteServer.Manage",
	"restart": "RemoteServer.Manage", "kill": "RemoteServer.Manage",
}

// remoteServerLifecycleHandler implements POST .../{start,stop,restart,kill}.
// It never simulates the outcome locally: the response reflects exactly
// what the remote node's own servers.Service reported back.
func (s *Server) remoteServerLifecycleHandler(w http.ResponseWriter, r *http.Request, nodeID, serverID, action string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	n, ok := s.requireEnabledRemoteNode(w, r, nodeID, "remote_server_management")
	if !ok {
		return
	}
	actor, summary, ok := s.authorizeRemoteServer(w, r, n, serverID, remoteLifecyclePermission[action], true)
	if !ok {
		return
	}
	var updated remote.ServerSummary
	var err error
	switch action {
	case "start":
		updated, err = s.remoteClient.StartServer(r.Context(), n.Endpoint, n.Credential, serverID)
	case "stop":
		updated, err = s.remoteClient.StopServer(r.Context(), n.Endpoint, n.Credential, serverID)
	case "restart":
		updated, err = s.remoteClient.RestartServer(r.Context(), n.Endpoint, n.Credential, serverID)
	case "kill":
		updated, err = s.remoteClient.KillServer(r.Context(), n.Endpoint, n.Credential, serverID)
	}
	auditAction := remoteLifecycleAction[action]
	if err != nil {
		s.recordRemoteServerAudit(r, actor, auditAction, audit.Failure, nodeID, serverID, summary.Name, nil, err)
		remoteServerError(w, err)
		return
	}
	s.recordRemoteServerAudit(r, actor, auditAction, audit.Success, nodeID, serverID, updated.Name, nil, nil)
	jsonOut(w, http.StatusOK, map[string]any{"remote_server": s.toPublicRemoteServer(r.Context(), nodeID, updated)})
}

// remoteServerConsoleHandler implements the bounded polling fallback and
// bounded input send .../console. The sibling /console/ws route handles the
// fixed JSON WebSocket relay. Console content is never audited - only the
// fact that input was sent (see AGENTS.md item 26/29).
func (s *Server) remoteServerConsoleHandler(w http.ResponseWriter, r *http.Request, nodeID, serverID string) {
	permission, csrfRequired := "RemoteConsole.View", false
	if r.Method == http.MethodPost {
		permission, csrfRequired = "RemoteConsole.Send", true
	} else if r.Method != http.MethodGet {
		method(w)
		return
	}
	n, ok := s.requireEnabledRemoteNode(w, r, nodeID, "remote_console")
	if !ok {
		return
	}
	actor, summary, ok := s.authorizeRemoteServer(w, r, n, serverID, permission, csrfRequired)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		snapshot, err := s.remoteClient.GetConsoleSnapshot(r.Context(), n.Endpoint, n.Credential, serverID)
		if err != nil {
			remoteServerError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, snapshot)
	case http.MethodPost:
		var in struct {
			Data string `json:"data"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Data == "" {
			bad(w, "console input is required")
			return
		}
		err := s.remoteClient.SendConsoleInput(r.Context(), n.Endpoint, n.Credential, serverID, in.Data)
		if err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteConsoleInput, audit.Failure, nodeID, serverID, summary.Name, map[string]any{"bytes": len(in.Data)}, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteConsoleInput, audit.Success, nodeID, serverID, summary.Name, map[string]any{"bytes": len(in.Data)}, nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

type remoteConsoleRelayClient interface {
	OpenConsoleRelay(context.Context, string, string, string) (*websocket.Conn, error)
}

type remoteBinaryFileClient interface {
	UploadFile(context.Context, string, string, string, string, string, io.Reader, bool) (remote.FileInfo, error)
	DownloadFile(context.Context, string, string, string, string) (remote.FileDownload, error)
}

// remoteServerConsoleRelayHandler keeps the local console JSON protocol at
// the browser boundary and forwards it over a second, machine-authenticated
// WebSocket to the target node. Browser permissions are checked again for
// every input and output event; the node remains the lifecycle/session owner.
func (s *Server) remoteServerConsoleRelayHandler(w http.ResponseWriter, r *http.Request, nodeID, serverID string) {
	if r.Method != http.MethodGet || !s.sameOrigin(r) {
		forbidden(w, "cross-origin request rejected")
		return
	}
	n, ok := s.requireEnabledRemoteNode(w, r, nodeID, "remote_console")
	if !ok {
		return
	}
	actor, summary, ok := s.authorizeRemoteServer(w, r, n, serverID, "RemoteConsole.View", false)
	if !ok {
		return
	}
	relay, ok := s.remoteClient.(remoteConsoleRelayClient)
	if !ok {
		errorOut(w, http.StatusNotImplemented, "node_capability_unsupported", "remote console relay is unavailable")
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: s.sameOrigin}
	local, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer local.Close()
	remoteConn, err := relay.OpenConsoleRelay(r.Context(), n.Endpoint, n.Credential, serverID)
	if err != nil {
		_ = writeConsole(local, consoleMessage{Type: "error", State: "remote_unavailable"})
		return
	}
	defer remoteConn.Close()
	local.SetReadLimit(console.MaxInputBytes + 1024)
	var writeMu sync.Mutex
	writeLocal := func(message consoleMessage) bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeConsole(local, message)
	}
	done := make(chan struct{})
	finish := func() {
		select {
		case <-done:
		default:
			close(done)
			_ = local.Close()
			_ = remoteConn.Close()
		}
	}
	go func() {
		defer finish()
		for {
			var in consoleClientMessage
			if err := local.ReadJSON(&in); err != nil {
				return
			}
			if in.Type != "input" || in.Data == "" || len([]byte(in.Data)) > console.MaxInputBytes {
				_ = writeLocal(consoleMessage{Type: "error", State: "invalid_input"})
				continue
			}
			allowed, err := s.allowedRemoteScope(r.Context(), actor, "RemoteConsole.Send", summary.TenantID)
			if err != nil || !allowed {
				_ = writeLocal(consoleMessage{Type: "error", State: "permission_denied"})
				continue
			}
			if err := remoteConn.WriteJSON(in); err != nil {
				return
			}
			s.recordRemoteServerAudit(r, actor, audit.RemoteConsoleInput, audit.Success, nodeID, serverID, summary.Name, map[string]any{"bytes": len([]byte(in.Data))}, nil)
		}
	}()
	for {
		var message consoleMessage
		if err := remoteConn.ReadJSON(&message); err != nil {
			finish()
			return
		}
		allowed, err := s.allowedRemoteScope(r.Context(), actor, "RemoteConsole.View", summary.TenantID)
		if err != nil || !allowed {
			finish()
			return
		}
		if !writeLocal(message) {
			finish()
			return
		}
	}
}

// remoteServerMonitoringHandler implements GET .../monitoring. Never
// audited - routine monitoring reads are not audited anywhere in this
// codebase (see AGENTS.md item 26).
func (s *Server) remoteServerMonitoringHandler(w http.ResponseWriter, r *http.Request, nodeID, serverID string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	n, ok := s.requireEnabledRemoteNode(w, r, nodeID, "remote_monitoring")
	if !ok {
		return
	}
	_, _, ok = s.authorizeRemoteServer(w, r, n, serverID, "RemoteMonitoring.View", false)
	if !ok {
		return
	}
	snapshot, err := s.remoteClient.GetMonitoringSnapshot(r.Context(), n.Endpoint, n.Credential, serverID)
	if err != nil {
		remoteServerError(w, err)
		return
	}
	jsonOut(w, http.StatusOK, snapshot)
}

// remoteServerFilesHandler implements the bounded remote files surface
// under .../files, forwarding to the remote node's own filesystem sandbox
// (see internal/api/node_servers.go). There is no "remote" special case in
// path handling here - the controller only ever forwards a path string; the
// remote node's own internal/filesystem.Service is exclusively responsible
// for sandboxing it. Binary upload/download passthrough is out of scope for
// this pass (text file content, listing, create, move/rename, delete are
// supported) - see docs/adr/0011-remote-operational-hardening.md.
func (s *Server) remoteServerFilesHandler(w http.ResponseWriter, r *http.Request, nodeID, serverID string, rest []string) {
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	permission, csrfRequired := "RemoteFiles.View", false
	switch {
	case sub == "" && r.Method == http.MethodDelete:
		permission, csrfRequired = "RemoteFiles.Delete", true
	case sub == "content" && r.Method == http.MethodPut:
		permission, csrfRequired = "RemoteFiles.Edit", true
	case sub == "file" && r.Method == http.MethodPost:
		permission, csrfRequired = "RemoteFiles.Edit", true
	case sub == "directory" && r.Method == http.MethodPost:
		permission, csrfRequired = "RemoteFiles.Edit", true
	case sub == "move" && r.Method == http.MethodPost:
		permission, csrfRequired = "RemoteFiles.Rename", true
	case sub == "upload" && r.Method == http.MethodPost:
		permission, csrfRequired = "RemoteFiles.Upload", true
	case sub == "download" && r.Method == http.MethodGet:
		permission, csrfRequired = "RemoteFiles.Download", false
	case (sub == "" || sub == "content") && r.Method == http.MethodGet:
	default:
		notFound(w)
		return
	}
	n, ok := s.requireEnabledRemoteNode(w, r, nodeID, "remote_files")
	if !ok {
		return
	}
	actor, summary, ok := s.authorizeRemoteServer(w, r, n, serverID, permission, csrfRequired)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	switch {
	case sub == "download" && r.Method == http.MethodGet:
		client, ok := s.remoteClient.(remoteBinaryFileClient)
		if !ok {
			errorOut(w, http.StatusNotImplemented, "node_capability_unsupported", "remote binary transfer is unavailable")
			return
		}
		download, err := client.DownloadFile(r.Context(), n.Endpoint, n.Credential, serverID, path)
		if err != nil {
			remoteServerError(w, err)
			return
		}
		defer download.Body.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": safeAttachmentName(path)}); disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		if download.Size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(download.Size, 10))
		}
		_, _ = io.Copy(w, download.Body)
		s.recordRemoteServerAudit(r, actor, audit.RemoteFileDownload, audit.Success, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(path)}, nil)
	case sub == "upload" && r.Method == http.MethodPost:
		client, ok := s.remoteClient.(remoteBinaryFileClient)
		if !ok {
			errorOut(w, http.StatusNotImplemented, "node_capability_unsupported", "remote binary transfer is unavailable")
			return
		}
		overwrite, err := parseOverwrite(r)
		if err != nil {
			bad(w, "invalid overwrite parameter")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, filesystem.DefaultMaxUploadBytes+(1<<20))
		reader, err := r.MultipartReader()
		if err != nil {
			bad(w, "multipart form data is required")
			return
		}
		part, err := reader.NextPart()
		if err != nil || part.FormName() != "file" || part.FileName() == "" {
			bad(w, "one file part is required")
			return
		}
		defer part.Close()
		info, err := client.UploadFile(r.Context(), n.Endpoint, n.Credential, serverID, path, part.FileName(), part, overwrite)
		if err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteFileUpload, audit.Failure, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(path)}, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteFileUpload, audit.Success, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(info.RelativePath), "size": info.Size}, nil)
		jsonOut(w, http.StatusCreated, info)
	case sub == "" && r.Method == http.MethodGet:
		entries, err := s.remoteClient.ListFiles(r.Context(), n.Endpoint, n.Credential, serverID, path)
		if err != nil {
			remoteServerError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"entries": entries})
	case sub == "" && r.Method == http.MethodDelete:
		recursive, _ := strconv.ParseBool(r.URL.Query().Get("recursive"))
		if err := s.remoteClient.DeleteFile(r.Context(), n.Endpoint, n.Credential, serverID, path, recursive); err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteFileDelete, audit.Failure, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(path)}, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteFileDelete, audit.Success, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(path)}, nil)
		w.WriteHeader(http.StatusNoContent)
	case sub == "content" && r.Method == http.MethodGet:
		content, err := s.remoteClient.ReadFile(r.Context(), n.Endpoint, n.Credential, serverID, path)
		if err != nil {
			remoteServerError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, content)
	case sub == "content" && r.Method == http.MethodPut:
		var in fileContentInput
		if !decodeFileMutation(w, r, &in) {
			return
		}
		if err := s.remoteClient.WriteFile(r.Context(), n.Endpoint, n.Credential, serverID, in.Path, in.Content); err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteFileEdit, audit.Failure, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(in.Path)}, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteFileEdit, audit.Success, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(in.Path)}, nil)
		w.WriteHeader(http.StatusNoContent)
	case sub == "file" && r.Method == http.MethodPost:
		var in fileContentInput
		if !decodeFileMutation(w, r, &in) {
			return
		}
		if err := s.remoteClient.CreateFile(r.Context(), n.Endpoint, n.Credential, serverID, in.Path, in.Content); err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteFileCreate, audit.Failure, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(in.Path)}, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteFileCreate, audit.Success, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(in.Path)}, nil)
		w.WriteHeader(http.StatusCreated)
	case sub == "directory" && r.Method == http.MethodPost:
		var in filePathInput
		if !decodeFileMutation(w, r, &in) {
			return
		}
		if err := s.remoteClient.CreateDirectory(r.Context(), n.Endpoint, n.Credential, serverID, in.Path); err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteFileCreate, audit.Failure, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(in.Path), "kind": "directory"}, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteFileCreate, audit.Success, nodeID, serverID, summary.Name, map[string]any{"path": auditRelativePath(in.Path), "kind": "directory"}, nil)
		w.WriteHeader(http.StatusCreated)
	case sub == "move" && r.Method == http.MethodPost:
		var in fileMoveInput
		if !decodeFileMutation(w, r, &in) {
			return
		}
		if err := s.remoteClient.MoveFile(r.Context(), n.Endpoint, n.Credential, serverID, in.Source, in.Destination); err != nil {
			s.recordRemoteServerAudit(r, actor, audit.RemoteFileMove, audit.Failure, nodeID, serverID, summary.Name, nil, err)
			remoteServerError(w, err)
			return
		}
		s.recordRemoteServerAudit(r, actor, audit.RemoteFileMove, audit.Success, nodeID, serverID, summary.Name, map[string]any{"from": auditRelativePath(in.Source), "to": auditRelativePath(in.Destination)}, nil)
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound(w)
	}
}
