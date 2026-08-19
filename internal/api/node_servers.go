package api

import (
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"gamenode/internal/console"
	"gamenode/internal/servers"
)

// This file implements the machine-authenticated Node-API surface a remote
// controller uses to manage THIS node's own local servers (v0.5B Remote
// Server Management / v0.5C Remote Operational Hardening - see
// docs/adr/0010-remote-server-lifecycle-forwarding.md and
// docs/adr/0011-remote-operational-hardening.md). Every handler here:
//   - requires requireMachineAuth (a durable machine credential, never a
//     browser session/CSRF - see AGENTS.md item 12/13);
//   - forwards to this node's OWN internal/servers.Service,
//     internal/filesystem.Service, internal/servers.Console(), and
//     MonitoringSnapshot - never a second, parallel lifecycle
//     implementation;
//   - returns a bounded, typed projection, never a raw Go error/stacktrace
//     and never a local filesystem path (working directory, executable
//     path, stop command) - AGENTS.md item 8/28.
//
// The node performs NO RBAC/tenant filtering of its own: a valid machine
// credential represents one fully trusted controller (identical to today's
// /api/v1/node/info contract), and the controller-side handlers
// (internal/api/remoteservers.go, remoteconsole.go, remotefiles.go,
// remotemonitoring.go) are solely responsible for browser RBAC/CSRF and for
// filtering what a given human operator may see or do.

const maxNodeServerListSize = 500

type nodeServerPatch struct {
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	AutoStart     *bool   `json:"auto_start,omitempty"`
	RestartPolicy *string `json:"restart_policy,omitempty"`
}

// nodeServer is the bounded, typed projection of servers.Record this node
// exposes to an authenticated remote controller. It deliberately excludes
// WorkingDirectory, Executable, Arguments, EnvironmentVariables,
// StopCommand, and Container - none of that ever needs to leave this
// machine (see AGENTS.md item 8).
type nodeServer struct {
	ID            string               `json:"id"`
	TenantID      string               `json:"tenant_id"`
	Name          string               `json:"name"`
	Description   string               `json:"description,omitempty"`
	CreationMode  string               `json:"creation_mode"`
	RuntimeType   string               `json:"runtime_type"`
	AutoStart     bool                 `json:"auto_start"`
	RestartPolicy string               `json:"restart_policy"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
	Runtime       servers.RuntimeState `json:"runtime"`
}

func toNodeServer(record servers.Record) nodeServer {
	return nodeServer{
		ID: record.Server.ID, TenantID: record.Server.TenantID, Name: record.Server.Name,
		Description: record.Server.Description, CreationMode: record.Server.CreationMode,
		RuntimeType: record.Server.RuntimeType, AutoStart: record.Server.AutoStart,
		RestartPolicy: record.Server.RestartPolicy, CreatedAt: record.Server.CreatedAt,
		UpdatedAt: record.Server.UpdatedAt, Runtime: record.Runtime,
	}
}

func nodeServerError(w http.ResponseWriter, err error) {
	serverError(w, err, false)
}

// nodeServersHandler implements GET/POST /api/v1/node/servers.
func (s *Server) nodeServersHandler(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineAuth(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.servers.List(r.Context())
		if err != nil {
			internal(w)
			return
		}
		if len(list) > maxNodeServerListSize {
			list = list[:maxNodeServerListSize]
		}
		out := make([]nodeServer, 0, len(list))
		for _, record := range list {
			out = append(out, toNodeServer(record))
		}
		jsonOut(w, http.StatusOK, map[string]any{"servers": out})
	case http.MethodPost:
		var in servers.Server
		if !decode(w, r, &in) {
			return
		}
		record, err := s.servers.Create(r.Context(), in)
		if err != nil {
			nodeServerError(w, err)
			return
		}
		jsonOut(w, http.StatusCreated, map[string]any{"server": toNodeServer(record)})
	default:
		method(w)
	}
}

// nodeServerHandler implements /api/v1/node/servers/{id} and its
// sub-resources (lifecycle, console, monitoring, files).
func (s *Server) nodeServerHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/node/servers/")
	parts := strings.Split(path, "/")
	if parts[0] == "" {
		notFound(w)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		if !s.requireMachineAuth(w, r) {
			return
		}
		s.nodeServerRootHandler(w, r, id)
		return
	}
	switch parts[1] {
	case "start", "stop", "restart", "kill":
		if !s.requireMachineAuth(w, r) {
			return
		}
		s.nodeServerLifecycleHandler(w, r, id, parts[1])
	case "console":
		if !s.requireMachineAuth(w, r) {
			return
		}
		s.nodeServerConsoleHandler(w, r, id, parts[2:])
	case "monitoring":
		if !s.requireMachineAuth(w, r) {
			return
		}
		s.nodeServerMonitoringHandler(w, r, id)
	case "files":
		if !s.requireMachineAuth(w, r) {
			return
		}
		s.nodeServerFilesHandler(w, r, id, parts[2:])
	default:
		notFound(w)
	}
}

func (s *Server) nodeServerRootHandler(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		record, err := s.servers.Get(r.Context(), id)
		if err != nil {
			nodeServerError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"server": toNodeServer(record)})
	case http.MethodPatch:
		var in nodeServerPatch
		if !decode(w, r, &in) {
			return
		}
		current, err := s.servers.Get(r.Context(), id)
		if err != nil {
			nodeServerError(w, err)
			return
		}
		updated := current.Server
		if in.Name != nil {
			updated.Name = *in.Name
		}
		if in.Description != nil {
			updated.Description = *in.Description
		}
		if in.AutoStart != nil {
			updated.AutoStart = *in.AutoStart
		}
		if in.RestartPolicy != nil {
			updated.RestartPolicy = *in.RestartPolicy
		}
		record, err := s.servers.Update(r.Context(), id, updated)
		if err != nil {
			nodeServerError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"server": toNodeServer(record)})
	case http.MethodDelete:
		if err := s.servers.Delete(r.Context(), id); err != nil {
			nodeServerError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}

func (s *Server) nodeServerLifecycleHandler(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var record servers.Record
	var err error
	switch action {
	case "start":
		record, err = s.servers.Start(r.Context(), id)
	case "stop":
		record, err = s.servers.Stop(r.Context(), id)
	case "restart":
		record, err = s.servers.Restart(r.Context(), id)
	case "kill":
		record, err = s.servers.Kill(r.Context(), id)
	}
	if err != nil {
		nodeServerError(w, err)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"server": toNodeServer(record)})
}

// nodeConsoleEvent is the bounded, typed projection of console.Event this
// node returns to a polling remote controller.
type nodeConsoleEvent struct {
	Type      string    `json:"type"`
	Stream    string    `json:"stream,omitempty"`
	Data      string    `json:"data,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

const maxNodeConsoleInputBytes = console.MaxInputBytes

// nodeServerConsoleHandler implements GET (bounded snapshot poll) and POST
// (bounded input send) /api/v1/node/servers/{id}/console. The sibling
// /console/ws route provides the fixed machine-authenticated relay. Console
// content is never audited (see AGENTS.md item 26/29).
func (s *Server) nodeServerConsoleHandler(w http.ResponseWriter, r *http.Request, id string, rest []string) {
	if len(rest) == 1 && rest[0] == "ws" {
		s.nodeServerConsoleWebSocketHandler(w, r, id)
		return
	}
	if len(rest) != 0 {
		notFound(w)
		return
	}
	if _, err := s.servers.Get(r.Context(), id); err != nil {
		nodeServerError(w, err)
		return
	}
	manager := s.servers.Console()
	switch r.Method {
	case http.MethodGet:
		session, attached := manager.CurrentSession(id)
		if !attached {
			session, attached = manager.LastClosedSession(id)
		}
		state := "closed"
		if manager.IsDetached(id) {
			state = "detached"
		}
		events := []nodeConsoleEvent{}
		if attached {
			raw, sessionState := session.Snapshot()
			state = sessionState
			for _, e := range raw {
				events = append(events, nodeConsoleEvent{Type: e.Type, Stream: e.Stream, Data: e.Data, Timestamp: e.Timestamp})
			}
		}
		jsonOut(w, http.StatusOK, map[string]any{"state": state, "events": events})
	case http.MethodPost:
		var in struct {
			Data string `json:"data"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Data == "" || len(in.Data) > maxNodeConsoleInputBytes {
			bad(w, "invalid console input")
			return
		}
		session, attached := manager.CurrentSession(id)
		if !attached {
			errorOut(w, http.StatusConflict, "invalid_state", "console is not attached")
			return
		}
		if err := session.Input(in.Data); err != nil {
			errorOut(w, http.StatusConflict, "invalid_state", "console input could not be delivered")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}

// nodeServerConsoleWebSocketHandler is the machine-authenticated half of a
// controller relay. The node owns the actual session and stdin; browser RBAC
// is enforced by the controller before it opens this connection.
func (s *Server) nodeServerConsoleWebSocketHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, err := s.servers.Get(r.Context(), id); err != nil {
		nodeServerError(w, err)
		return
	}
	manager := s.servers.Console()
	session, attached := manager.CurrentSession(id)
	if !attached {
		if manager.IsDetached(id) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err == nil {
				defer conn.Close()
				_ = writeConsole(conn, consoleMessage{Type: "console", State: "detached"})
			}
			return
		}
		session, attached = manager.LastClosedSession(id)
		if !attached {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err == nil {
				defer conn.Close()
				_ = writeConsole(conn, consoleMessage{Type: "console", State: "closed"})
			}
			return
		}
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(console.MaxInputBytes + 1024)
	if !writeConsole(conn, consoleMessage{Type: "console", State: "attached"}) {
		return
	}
	events, unsubscribe := session.Subscribe()
	defer unsubscribe()
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		for {
			var in consoleClientMessage
			if err := conn.ReadJSON(&in); err != nil {
				return
			}
			if in.Type != "input" || in.Data == "" || len([]byte(in.Data)) > console.MaxInputBytes {
				continue
			}
			_ = session.Input(in.Data)
		}
	}()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if !writeConsole(conn, consoleMessage{Type: event.Type, Stream: event.Stream, Data: event.Data, State: event.State, Timestamp: event.Timestamp}) {
				return
			}
		case <-inputDone:
			return
		}
	}
}

// nodeServerMonitoringHandler implements GET /api/v1/node/servers/{id}/monitoring.
func (s *Server) nodeServerMonitoringHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	snapshot, err := s.servers.MonitoringSnapshot(r.Context(), id)
	if err != nil {
		nodeServerError(w, err)
		return
	}
	jsonOut(w, http.StatusOK, snapshot)
}

// nodeServerFilesHandler implements the bounded remote filesystem surface
// under /api/v1/node/servers/{id}/files, forwarding every call to this
// node's own internal/filesystem sandbox rooted at the server's working
// directory. There is no "remote" special case in the sandbox itself (see
// AGENTS.md's rule that Remote Files must use the same central sandbox as
// local). Binary upload/download passthrough is out of scope for this pass
// (see the ADR) - only text file content, directory listing, create,
// move/rename, and delete are exposed remotely.
func (s *Server) nodeServerFilesHandler(w http.ResponseWriter, r *http.Request, id string, rest []string) {
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		nodeServerError(w, err)
		return
	}
	root := record.Server.WorkingDirectory
	sub := ""
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch {
	case sub == "download" && r.Method == http.MethodGet:
		file, info, err := s.files.OpenDownload(root, r.URL.Query().Get("path"))
		if err != nil {
			filesystemError(w, err)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": safeAttachmentName(info.RelativePath)}); disposition != "" {
			w.Header().Set("Content-Disposition", disposition)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		_, _ = io.Copy(w, file)
	case sub == "upload" && r.Method == http.MethodPost:
		overwrite, err := parseOverwrite(r)
		if err != nil {
			bad(w, "invalid overwrite parameter")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.files.MaxUploadBytes()+(1<<20))
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
		info, err := s.files.Upload(root, r.URL.Query().Get("path"), part.FileName(), part, overwrite)
		if err != nil {
			filesystemError(w, err)
			return
		}
		jsonOut(w, http.StatusCreated, info)
	case sub == "" && r.Method == http.MethodGet:
		entries, err := s.files.ListDirectory(root, r.URL.Query().Get("path"))
		if err != nil {
			filesystemError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"entries": entries})
	case sub == "" && r.Method == http.MethodDelete:
		recursive, _ := strconv.ParseBool(r.URL.Query().Get("recursive"))
		if err := s.files.Delete(root, r.URL.Query().Get("path"), recursive); err != nil {
			filesystemError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case sub == "content" && r.Method == http.MethodGet:
		content, err := s.files.ReadFile(root, r.URL.Query().Get("path"))
		if err != nil {
			filesystemError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, content)
	case sub == "content" && r.Method == http.MethodPut:
		var in fileContentInput
		if !decodeFileMutation(w, r, &in) {
			return
		}
		if err := s.files.WriteFile(root, in.Path, in.Content); err != nil {
			filesystemError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case sub == "file" && r.Method == http.MethodPost:
		var in fileContentInput
		if !decodeFileMutation(w, r, &in) {
			return
		}
		if err := s.files.CreateFile(root, in.Path, in.Content); err != nil {
			filesystemError(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	case sub == "directory" && r.Method == http.MethodPost:
		var in filePathInput
		if !decodeFileMutation(w, r, &in) {
			return
		}
		if err := s.files.CreateDirectory(root, in.Path); err != nil {
			filesystemError(w, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
	case sub == "move" && r.Method == http.MethodPost:
		var in fileMoveInput
		if !decodeFileMutation(w, r, &in) {
			return
		}
		if err := s.files.Move(root, in.Source, in.Destination); err != nil {
			filesystemError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound(w)
	}
}
