package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/filesystem"
	"gamenode/internal/logging"
	"gamenode/internal/rbac"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/tenants"
)

const maxFileMutationRequestBytes = filesystem.MaxReadBytes*6 + 64<<10

func (s *Server) recordServerAudit(r *http.Request, actor auth.User, action, result, id, name string, err error) {
	var resourceID *string
	var serverID *string
	if id != "" {
		resourceID = &id
		serverID = &id
	}
	in := auditInput{action: action, resourceType: audit.Server, resourceID: resourceID, resourceName: name, serverID: serverID, result: result, actor: &actor}
	if err != nil {
		in.errorCode, in.errorSummary = auditFailure(err)
		in.err = err
	}
	s.recordAudit(r, in)
}

func (s *Server) recordServerTenantMigrationAudit(r *http.Request, actor auth.User, result, serverID, serverName, fromTenantID, toTenantID string, err error) {
	metadata, _ := json.Marshal(map[string]string{"from_tenant_id": fromTenantID, "to_tenant_id": toTenantID})
	in := auditInput{action: audit.ServerTenantMigrate, resourceType: audit.Server, resourceID: &serverID, resourceName: serverName, serverID: &serverID, result: result, metadata: metadata, actor: &actor}
	if err != nil {
		in.errorCode, in.errorSummary = auditFailure(err)
		in.err = err
	}
	s.recordAudit(r, in)
}

type fileContentInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type filePathInput struct {
	Path string `json:"path"`
}

type fileMoveInput struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func auditRelativePath(value string) string {
	clean := path.Clean(strings.ReplaceAll(value, "\\", "/"))
	if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}

func (s *Server) recordFileAudit(r *http.Request, actor auth.User, action, result, serverID, resourceName string, metadata map[string]any, err error) {
	server := serverID
	in := auditInput{action: action, resourceType: audit.File, resourceName: resourceName, serverID: &server, result: result, actor: &actor}
	if result == audit.Success && metadata != nil {
		in.metadata, _ = json.Marshal(metadata)
	}
	if err != nil {
		in.errorCode, in.errorSummary = auditFailure(err)
	}
	s.recordAudit(r, in)
}

func (s *Server) serversHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		u, _, ok := s.requireAuth(w, r, false)
		if !ok {
			return
		}
		records, err := s.visibleServers(r.Context(), u)
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"servers": records})
	case http.MethodPost:
		// This endpoint accepts an arbitrary admin-supplied WorkingDirectory
		// (Custom Application, Adopt Existing) and therefore stays global
		// Server.Create only, deliberately, even though the permission
		// itself now also supports tenant scope. A tenant-scoped
		// Server.Create grant must never be able to register or point a
		// server at an arbitrary host path; it is honored only by the
		// managed template/SteamCMD provisioning flow (see
		// internal/api/provisioning.go and internal/tenants.TenantServerRoot),
		// which never accepts a host path from the client. See
		// docs/architecture.md's Tenant Foundation Step 4 section.
		u, _, ok := s.requirePermission(w, r, "Server.Create", rbac.Scope{Type: "global"}, true)
		if !ok {
			return
		}
		var server servers.Server
		if !decode(w, r, &server) {
			return
		}
		record, err := s.servers.Create(r.Context(), server)
		if err != nil {
			s.recordServerAudit(r, u, audit.ServerCreate, audit.Failure, "", "", err)
			code, _ := auditFailure(err)
			s.log.With("module", "Server.Create").Error("server creation failed", "failure", code, "actor_user_id", u.ID)
			serverError(w, err, false)
			return
		}
		s.recordServerAudit(r, u, audit.ServerCreate, audit.Success, record.Server.ID, record.Server.Name, nil)
		s.log.With("module", "Server.Create").Info("server created", "server_id", record.Server.ID, "mode", record.Server.CreationMode)
		record, err = s.publicServerRecord(r.Context(), record)
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusCreated, map[string]any{"server": record.Server, "runtime": record.Runtime, "tenant_name": s.tenantName(r.Context(), record.Server.TenantID)})
	default:
		method(w)
	}
}

// creatableTenantsHandler implements GET /api/v1/servers/creatable-tenants:
// the tenants the current authenticated user may create a managed server
// in. This exists so the Create Server / Game Library UI can offer or lock
// a tenant selector without requiring Tenants.View, which a plain
// tenant-scoped operator does not hold - it reveals only tenant id/name,
// the same information already shown on every server record the user can
// see. A global Server.Create grant (or admin bypass) returns every tenant;
// a tenant-scoped grant returns only its own tenant(s); no grant returns an
// empty list, which the UI treats as "no create action available".
func (s *Server) creatableTenantsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	u, _, ok := s.requireAuth(w, r, false)
	if !ok {
		return
	}
	list, err := s.tenants.List(r.Context())
	if err != nil {
		internal(w)
		return
	}
	result := make([]tenants.Tenant, 0, len(list))
	for _, tenant := range list {
		tenantID := tenant.ID
		allowed, err := s.allowed(r.Context(), u, "Server.Create", rbac.Scope{Type: "tenant", ID: &tenantID})
		if err != nil {
			internal(w)
			return
		}
		if allowed {
			result = append(result, tenant)
		}
	}
	jsonOut(w, http.StatusOK, map[string]any{"tenants": result})
}

func (s *Server) serverHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 3 {
		notFound(w)
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "tenant" {
		s.serverTenantMigrationHandler(w, r, id)
		return
	}
	if len(parts) >= 2 && parts[1] == "restart-schedules" {
		s.restartSchedulesHandler(w, r, id, parts[2:])
		return
	}
	if len(parts) >= 2 && parts[1] == "ftp" {
		s.ftpHandler(w, r, id, parts[2:])
		return
	}
	if len(parts) == 2 && parts[1] == "access" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
			return
		}
		assignments, err := s.rbac.ListServerAssignments(r.Context(), id)
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"assignments": assignments})
		return
	}
	if len(parts) == 2 && parts[1] == "files" {
		s.filesHandler(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "monitoring" {
		s.monitoringHandler(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "configuration" {
		s.gameConfigurationHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "container" && parts[2] == "pull" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		u, _, ok := s.requireServerPermission(w, r, "Server.Edit", id, true)
		if !ok {
			return
		}
		if err := s.servers.PullContainerImage(r.Context(), id); err != nil {
			s.recordServerAudit(r, u, audit.ServerUpdate, audit.Failure, id, "", err)
			serverError(w, err, false)
			return
		}
		record, err := s.servers.Get(r.Context(), id)
		if err != nil {
			internal(w)
			return
		}
		s.recordServerAudit(r, u, audit.ServerUpdate, audit.Success, id, record.Server.Name, nil)
		jsonOut(w, http.StatusOK, map[string]string{"image_status": "ready"})
		return
	}
	if len(parts) >= 2 && parts[1] == "ports" {
		s.portsHandler(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "update" {
		s.serverUpdateHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "monitoring" && parts[2] == "history" {
		s.monitoringHistoryHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "files" && parts[2] == "content" {
		s.filesContentHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "files" && parts[2] == "file" {
		s.filesCreateFileHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "files" && parts[2] == "directory" {
		s.filesCreateDirectoryHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "files" && parts[2] == "move" {
		s.filesMoveHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "files" && parts[2] == "download" {
		s.filesDownloadHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "files" && parts[2] == "upload" {
		s.filesUploadHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "console" && parts[2] == "ws" {
		s.consoleWS(w, r, id)
		return
	}
	if len(parts) == 1 {
		permission := "Server.View"
		csrfRequired := false
		switch r.Method {
		case http.MethodPatch:
			permission, csrfRequired = "Server.Edit", true
		case http.MethodDelete:
			permission, csrfRequired = "Server.Delete", true
		case http.MethodGet:
		default:
			method(w)
			return
		}
		u, _, ok := s.requireServerPermission(w, r, permission, id, csrfRequired)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			record, err := s.servers.Get(r.Context(), id)
			if err != nil {
				serverError(w, err, false)
				return
			}
			capabilities, err := s.serverCapabilities(r.Context(), u, id)
			if err != nil {
				internal(w)
				return
			}
			record, err = s.publicServerRecord(r.Context(), record)
			if err != nil {
				internal(w)
				return
			}
			jsonOut(w, http.StatusOK, map[string]any{"server": record.Server, "runtime": record.Runtime, "capabilities": capabilities, "tenant_name": s.tenantName(r.Context(), record.Server.TenantID)})
		case http.MethodPatch:
			var server servers.Server
			if !decode(w, r, &server) {
				return
			}
			// server.TenantID is whatever the client sent (or left empty);
			// servers.Store.Update always preserves the existing tenant
			// regardless, so a PATCH body can never move a server between
			// tenants (see internal/servers.Store.Update and
			// GameNode_Tenant_Foundation_Prompt.md section 4.8).
			record, err := s.servers.Update(r.Context(), id, server)
			if err != nil {
				s.recordServerAudit(r, u, audit.ServerUpdate, audit.Failure, id, "", err)
				serverError(w, err, false)
				return
			}
			s.recordServerAudit(r, u, audit.ServerUpdate, audit.Success, record.Server.ID, record.Server.Name, nil)
			s.log.With("module", "Server.Update").Info("server updated", "server_id", id)
			record, err = s.publicServerRecord(r.Context(), record)
			if err != nil {
				internal(w)
				return
			}
			jsonOut(w, http.StatusOK, map[string]any{"server": record.Server, "runtime": record.Runtime, "tenant_name": s.tenantName(r.Context(), record.Server.TenantID)})
		case http.MethodDelete:
			name := ""
			if existing, err := s.servers.Get(r.Context(), id); err == nil {
				name = existing.Server.Name
			}
			if err := s.servers.Delete(r.Context(), id); err != nil {
				s.recordServerAudit(r, u, audit.ServerDelete, audit.Failure, id, name, err)
				serverError(w, err, true)
				return
			}
			s.recordServerAudit(r, u, audit.ServerDelete, audit.Success, id, name, nil)
			s.log.With("module", "Server.Delete").Info("server deleted", "server_id", id)
			w.WriteHeader(http.StatusNoContent)
		default:
			method(w)
		}
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var record servers.Record
	var err error
	permission := ""
	switch parts[1] {
	case "start":
		permission = "Server.Start"
	case "stop":
		permission = "Server.Stop"
	case "restart":
		permission = "Server.Restart"
	case "kill":
		permission = "Server.Kill"
	default:
		notFound(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, permission, id, true)
	if !ok {
		return
	}
	switch parts[1] {
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
		action := map[string]string{"start": audit.ServerStart, "stop": audit.ServerStop, "restart": audit.ServerRestart, "kill": audit.ServerKill}[parts[1]]
		name := ""
		if current, getErr := s.servers.Get(r.Context(), id); getErr == nil {
			name = current.Server.Name
		}
		s.recordServerAudit(r, u, action, audit.Failure, id, name, err)
		serverError(w, err, true)
		return
	}
	action := map[string]string{"start": audit.ServerStart, "stop": audit.ServerStop, "restart": audit.ServerRestart, "kill": audit.ServerKill}[parts[1]]
	s.recordServerAudit(r, u, action, audit.Success, record.Server.ID, record.Server.Name, nil)
	module := map[string]string{"start": "Server.Start", "stop": "Server.Stop", "restart": "Server.Restart", "kill": "Server.Kill"}[parts[1]]
	s.log.With("module", module).Info("server lifecycle action", "server_id", id)
	record, err = s.publicServerRecord(r.Context(), record)
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, record)
}

func (s *Server) serverTenantMigrationHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	actor, _, ok := s.requireAuth(w, r, true)
	if !ok {
		return
	}
	if !actor.IsAdmin {
		forbidden(w, "administrator access required")
		return
	}
	var input struct {
		TenantID string `json:"tenant_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	targetTenantID := strings.TrimSpace(input.TenantID)
	if targetTenantID == "" {
		bad(w, "tenant_id is required")
		return
	}
	existing, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	if _, err = s.tenants.Get(r.Context(), targetTenantID); err != nil {
		if errors.Is(err, tenants.ErrTenantNotFound) || errors.Is(err, sql.ErrNoRows) {
			notFound(w)
		} else {
			internal(w)
		}
		s.recordServerTenantMigrationAudit(r, actor, audit.Failure, id, existing.Server.Name, existing.Server.TenantID, targetTenantID, err)
		return
	}
	record, err := s.servers.MigrateTenant(r.Context(), id, targetTenantID)
	if err != nil {
		s.recordServerTenantMigrationAudit(r, actor, audit.Failure, id, existing.Server.Name, existing.Server.TenantID, targetTenantID, err)
		serverError(w, err, true)
		return
	}
	s.recordServerTenantMigrationAudit(r, actor, audit.Success, id, record.Server.Name, existing.Server.TenantID, record.Server.TenantID, nil)
	record, err = s.publicServerRecord(r.Context(), record)
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"server": record.Server, "runtime": record.Runtime, "tenant_name": s.tenantName(r.Context(), record.Server.TenantID)})
}

func (s *Server) filesHandler(w http.ResponseWriter, r *http.Request, id string) {
	permission, csrfRequired := "Files.View", false
	if r.Method == http.MethodDelete {
		permission, csrfRequired = "Files.Delete", true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		method(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, permission, id, csrfRequired)
	if !ok {
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	switch r.Method {
	case http.MethodGet:
		entries, err := s.files.ListDirectory(record.Server.WorkingDirectory, r.URL.Query().Get("path"))
		if err != nil {
			filesystemError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"entries": entries})
	case http.MethodDelete:
		recursive, err := recursiveDelete(r)
		if err != nil {
			bad(w, "invalid recursive parameter")
			return
		}
		if err := s.files.Delete(record.Server.WorkingDirectory, r.URL.Query().Get("path"), recursive); err != nil {
			s.recordFileAudit(r, u, audit.FileDelete, audit.Failure, id, "", nil, err)
			filesystemError(w, err)
			return
		}
		name := auditRelativePath(r.URL.Query().Get("path"))
		s.recordFileAudit(r, u, audit.FileDelete, audit.Success, id, name, map[string]any{"path": name, "recursive": recursive}, nil)
		s.logFileMutation("file.delete", id)
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}

func (s *Server) filesContentHandler(w http.ResponseWriter, r *http.Request, id string) {
	permission, csrfRequired := "Files.View", false
	if r.Method == http.MethodPut {
		permission, csrfRequired = "Files.Edit", true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		method(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, permission, id, csrfRequired)
	if !ok {
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	switch r.Method {
	case http.MethodGet:
		content, err := s.files.ReadFile(record.Server.WorkingDirectory, r.URL.Query().Get("path"))
		if err != nil {
			filesystemError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, content)
	case http.MethodPut:
		var input fileContentInput
		if !decodeFileMutation(w, r, &input) {
			return
		}
		if err := s.files.WriteFile(record.Server.WorkingDirectory, input.Path, input.Content); err != nil {
			s.recordFileAudit(r, u, audit.FileEdit, audit.Failure, id, "", nil, err)
			filesystemError(w, err)
			return
		}
		name := auditRelativePath(input.Path)
		s.recordFileAudit(r, u, audit.FileEdit, audit.Success, id, name, map[string]any{"path": name, "bytes": len([]byte(input.Content))}, nil)
		s.logFileMutation("file.edit", id)
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}

func (s *Server) filesCreateFileHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, "Files.Edit", id, true)
	if !ok {
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	var input fileContentInput
	if !decodeFileMutation(w, r, &input) {
		return
	}
	if err := s.files.CreateFile(record.Server.WorkingDirectory, input.Path, input.Content); err != nil {
		s.recordFileAudit(r, u, audit.FileCreate, audit.Failure, id, "", nil, err)
		filesystemError(w, err)
		return
	}
	name := auditRelativePath(input.Path)
	s.recordFileAudit(r, u, audit.FileCreate, audit.Success, id, name, map[string]any{"path": name}, nil)
	s.logFileMutation("file.create", id)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) filesCreateDirectoryHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, "Files.Edit", id, true)
	if !ok {
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	var input filePathInput
	if !decodeFileMutation(w, r, &input) {
		return
	}
	if err := s.files.CreateDirectory(record.Server.WorkingDirectory, input.Path); err != nil {
		s.recordFileAudit(r, u, audit.FileCreate, audit.Failure, id, "", nil, err)
		filesystemError(w, err)
		return
	}
	name := auditRelativePath(input.Path)
	s.recordFileAudit(r, u, audit.FileCreate, audit.Success, id, name, map[string]any{"path": name, "kind": "directory"}, nil)
	s.logFileMutation("directory.create", id)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) filesMoveHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, "Files.Rename", id, true)
	if !ok {
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	var input fileMoveInput
	if !decodeFileMutation(w, r, &input) {
		return
	}
	if err := s.files.Move(record.Server.WorkingDirectory, input.Source, input.Destination); err != nil {
		s.recordFileAudit(r, u, audit.FileMove, audit.Failure, id, "", nil, err)
		filesystemError(w, err)
		return
	}
	from, to := auditRelativePath(input.Source), auditRelativePath(input.Destination)
	action := audit.FileMove
	if path.Dir(from) == path.Dir(to) {
		action = audit.FileRename
	}
	s.recordFileAudit(r, u, action, audit.Success, id, to, map[string]any{"from": from, "to": to}, nil)
	s.logFileMutation(action, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) filesDownloadHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireServerPermission(w, r, "Files.Download", id, false); !ok {
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	file, info, err := s.files.OpenDownload(record.Server.WorkingDirectory, r.URL.Query().Get("path"))
	if err != nil {
		filesystemError(w, err)
		return
	}
	defer file.Close()
	filename := safeAttachmentName(info.RelativePath)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if disposition == "" {
		disposition = `attachment; filename="download"`
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
	s.logFileMutation("file.download", id)
}

func (s *Server) filesUploadHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, "Files.Upload", id, true)
	if !ok {
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
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
	info, err := s.files.Upload(record.Server.WorkingDirectory, r.URL.Query().Get("path"), part.FileName(), part, overwrite)
	if err != nil {
		s.recordFileAudit(r, u, audit.FileUpload, audit.Failure, id, "", nil, err)
		filesystemError(w, err)
		return
	}
	s.recordFileAudit(r, u, audit.FileUpload, audit.Success, id, info.RelativePath, map[string]any{"path": info.RelativePath, "filename": path.Base(info.RelativePath), "size": info.Size}, nil)
	s.logFileMutation("file.upload", id)
	jsonOut(w, http.StatusCreated, info)
}

func parseOverwrite(r *http.Request) (bool, error) {
	value := r.URL.Query().Get("overwrite")
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

func safeAttachmentName(relativePath string) string {
	name := path.Base(relativePath)
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return "download"
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return "download"
		}
	}
	return name
}

func decodeFileMutation(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFileMutationRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		bad(w, "invalid request body")
		return false
	}
	return true
}

func recursiveDelete(r *http.Request) (bool, error) {
	value := r.URL.Query().Get("recursive")
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

// Future Files.Edit/Delete/Rename authorization can attach to these action
// names without coupling permissions to the filesystem implementation.
func (s *Server) logFileMutation(action, serverID string) {
	s.log.With("module", "Files.Mutation", "category", logging.CategoryFilesystem).Info("file mutation", "action", action, "server_id", serverID)
}

func filesystemError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, filesystem.ErrInvalidPath), errors.Is(err, filesystem.ErrInvalidFilename), errors.Is(err, filesystem.ErrPathEscapesRoot), errors.Is(err, filesystem.ErrExpectedFile), errors.Is(err, filesystem.ErrExpectedDir), errors.Is(err, filesystem.ErrSpecialFile):
		errorOut(w, http.StatusBadRequest, "invalid_path", "path is not available")
	case errors.Is(err, filesystem.ErrRootOperation):
		errorOut(w, http.StatusBadRequest, "invalid_path", "server root cannot be modified")
	case errors.Is(err, filesystem.ErrAlreadyExists), errors.Is(err, filesystem.ErrDirectoryNotEmpty):
		errorOut(w, http.StatusConflict, "file_conflict", "filesystem operation conflicts with existing content")
	case errors.Is(err, filesystem.ErrNotFound):
		errorOut(w, http.StatusNotFound, "not_found", "path not found")
	case errors.Is(err, filesystem.ErrTooLarge):
		errorOut(w, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the read limit")
	case errors.Is(err, filesystem.ErrBinaryFile):
		errorOut(w, http.StatusUnsupportedMediaType, "unsupported_file", "binary files cannot be read as text")
	case errors.Is(err, fs.ErrPermission):
		forbidden(w, "file access denied")
	default:
		internal(w)
	}
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request, csrf bool) (string, string, bool) {
	u, token, ok := s.requireAuth(w, r, csrf)
	if !ok {
		return "", "", false
	}
	if !u.IsAdmin {
		forbidden(w, "administrator access required")
		return "", "", false
	}
	return u.ID, token, true
}

func serverError(w http.ResponseWriter, err error, conflict bool) {
	if errors.Is(err, sql.ErrNoRows) {
		errorOut(w, http.StatusNotFound, "not_found", "server not found")
		return
	}
	if errors.Is(err, servers.ErrTenantMigrationUnsupported) {
		errorOut(w, http.StatusConflict, "tenant_migration_unsupported", "only provisioned servers can be physically migrated between tenants")
		return
	}
	if errors.Is(err, servers.ErrTenantMigrationStorage) {
		errorOut(w, http.StatusServiceUnavailable, "tenant_migration_unavailable", "managed server storage migration is unavailable")
		return
	}
	if errors.Is(err, servers.ErrTenantMigrationPath) {
		errorOut(w, http.StatusConflict, "tenant_migration_invalid_path", "the provisioned server is not located in its managed tenant storage")
		return
	}
	if errors.Is(err, filesystem.ErrAlreadyExists) {
		errorOut(w, http.StatusConflict, "tenant_migration_target_exists", "the target tenant already contains a server directory with this name")
		return
	}
	// Console-interrupt outcomes get their own safe, specific messages instead
	// of the generic conflict text below; they never carry OS error text,
	// PIDs, or handles. See docs/security.md.
	if errors.Is(err, runtime.ErrConsoleInterruptUnsupported) {
		errorOut(w, http.StatusConflict, runtime.CodeConsoleInterruptUnsupported, "Windows console interrupt is unavailable for this process")
		return
	}
	if errors.Is(err, runtime.ErrConsoleInterruptFailed) {
		errorOut(w, http.StatusConflict, runtime.CodeConsoleInterruptFailed, "Windows console interrupt could not be delivered")
		return
	}
	// servers.ErrDuplicateName is always this sanitized sentinel, never raw
	// SQL/driver text (see servers.Store.NameAvailable/classifyNameConstraint),
	// so it is safe to surface directly instead of falling through to the
	// generic bad() below.
	if errors.Is(err, servers.ErrDuplicateName) {
		errorOut(w, http.StatusConflict, "name_conflict", err.Error())
		return
	}
	if conflict {
		errorOut(w, http.StatusConflict, "invalid_state", "server state does not allow this operation")
		return
	}
	bad(w, err.Error())
}
func notFound(w http.ResponseWriter) {
	errorOut(w, http.StatusNotFound, "not_found", "resource not found")
}
