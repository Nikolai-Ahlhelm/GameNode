package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/rbac"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

const maxEggEnvelopeBytes = templates.MaxEggBytes + 4096

type eggInput struct {
	Egg json.RawMessage `json:"egg"`
}

type neoForgeInput struct {
	ServerName      string `json:"server_name"`
	ServerRoot      string `json:"server_root"`
	MinimumMemoryMB int    `json:"minimum_memory_mb"`
	MaximumMemoryMB int    `json:"maximum_memory_mb"`
	NoGUI           *bool  `json:"nogui,omitempty"`
}

func (s *Server) neoForgeTemplateAction(w http.ResponseWriter, r *http.Request, templateID, action string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Templates.View", action == "adopt"); !ok {
		return
	}
	actor, _, ok := s.requirePermission(w, r, "Server.Create", rbac.Scope{Type: "global"}, action == "adopt")
	if !ok {
		return
	}
	template, err := s.templates.Get(r.Context(), templateID)
	if err != nil || (template.SourceType != templates.SourceBuiltin && template.SourceType != templates.SourceOfficial) || template.Launch == nil || template.Launch.Resolver != "neoforge" {
		notFound(w)
		return
	}
	var input neoForgeInput
	if !decode(w, r, &input) {
		return
	}
	if input.MinimumMemoryMB == 0 {
		input.MinimumMemoryMB = 1024
	}
	if input.MaximumMemoryMB == 0 {
		input.MaximumMemoryMB = 4096
	}
	nogui := true
	if input.NoGUI != nil {
		nogui = *input.NoGUI
	}
	resolved, err := templates.ResolveNeoForge(input.ServerRoot, runtime.GOOS, input.MinimumMemoryMB, input.MaximumMemoryMB, nogui)
	if err != nil {
		errorOut(w, http.StatusUnprocessableEntity, "neoforge_resolution_failed", err.Error())
		return
	}
	if action == "resolve" {
		jsonOut(w, http.StatusOK, resolved)
		return
	}
	if !resolved.JavaFound {
		errorOut(w, http.StatusUnprocessableEntity, "java_not_found", "Java was not found through JAVA_HOME or PATH")
		return
	}
	server := servers.Server{CreationMode: servers.CreationTemplate, Name: strings.TrimSpace(input.ServerName), Description: template.Description, WorkingDirectory: resolved.WorkingDirectory, Executable: resolved.Executable, Arguments: resolved.Arguments, EnvironmentVariables: map[string]string{}, RuntimeType: "native", RestartPolicy: "never", StopMethod: resolved.StopMethod, StopCommand: resolved.StopCommand, StopTimeoutSeconds: resolved.StopTimeout, AutoRestartMaxAttempts: 3, AutoRestartWindowSeconds: 300, AutoRestartDelaySeconds: 5}
	metadata := []servers.ProvisionedVariable{{Key: "MIN_MEMORY_MB", Source: template.SourceType, Version: template.Version}, {Key: "MAX_MEMORY_MB", Source: template.SourceType, Version: template.Version}, {Key: "NOGUI", Source: template.SourceType, Version: template.Version}}
	record, err := s.servers.CreateProvisioned(r.Context(), server, template.ID, metadata, nil, nil)
	if err != nil {
		s.recordServerAudit(r, actor, audit.ServerCreate, audit.Failure, "", server.Name, err)
		serverError(w, err, false)
		return
	}
	s.recordServerAudit(r, actor, audit.ServerCreate, audit.Success, record.Server.ID, record.Server.Name, nil)
	jsonOut(w, http.StatusCreated, record)
}

func (s *Server) templateCatalogHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Templates.View", false); !ok {
		return
	}
	jsonOut(w, http.StatusOK, s.templates.Catalog())
}

func (s *Server) templateCatalogRefreshHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Templates.View", true); !ok {
		return
	}
	result, err := s.templates.RefreshCatalog(r.Context())
	if err != nil && len(result.Templates) == 0 {
		errorOut(w, http.StatusServiceUnavailable, "official_catalog_unavailable", "Official Game Library is unavailable and no valid cache exists")
		return
	}
	jsonOut(w, http.StatusOK, result)
}

func decodeEggInput(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxEggEnvelopeBytes+1))
	if err != nil {
		bad(w, "invalid request body")
		return nil, false
	}
	if len(data) > maxEggEnvelopeBytes {
		errorOut(w, http.StatusRequestEntityTooLarge, "egg_too_large", "egg import exceeds the size limit")
		return nil, false
	}
	var input eggInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(input.Egg) == 0 {
		bad(w, "invalid egg import request")
		return nil, false
	}
	if len(input.Egg) > templates.MaxEggBytes {
		errorOut(w, http.StatusRequestEntityTooLarge, "egg_too_large", "egg import exceeds the size limit")
		return nil, false
	}
	return input.Egg, true
}

func templateAuditMetadata(template templates.Template) json.RawMessage {
	metadata, _ := json.Marshal(map[string]any{"source_type": template.SourceType, "compatibility_status": template.Compatibility.Status, "variable_count": len(template.Variables), "installer_type": template.Installer.Type})
	return metadata
}
func (s *Server) recordTemplateAudit(r *http.Request, actor auth.User, action, result string, template templates.Template, err error) {
	var id *string
	if template.ID != "" {
		id = &template.ID
	}
	input := auditInput{action: action, resourceType: audit.Template, resourceID: id, resourceName: template.Name, result: result, actor: &actor}
	if result == audit.Success {
		input.metadata = templateAuditMetadata(template)
	}
	if err != nil {
		input.errorCode = "template_operation_failed"
		input.errorSummary = "template operation failed"
	}
	s.recordAudit(r, input)
}

func (s *Server) templatesHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Templates.View", false); !ok {
			return
		}
		items, err := s.templates.List(r.Context())
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"templates": items})
	default:
		method(w)
	}
}

func (s *Server) templateHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/templates/")
	if parts := strings.Split(path, "/"); len(parts) == 2 && parts[0] != "" && (parts[1] == "resolve" || parts[1] == "adopt") {
		s.neoForgeTemplateAction(w, r, parts[0], parts[1])
		return
	}
	if parts := strings.Split(path, "/"); len(parts) == 2 && parts[0] != "" && parts[1] == "provision" {
		s.startProvisioning(w, r, parts[0])
		return
	}
	if parts := strings.Split(path, "/"); len(parts) == 2 && parts[0] != "" && parts[1] == "provisionability" {
		s.templateProvisionability(w, r, parts[0])
		return
	}
	if path == "analyze/egg" || path == "import/egg" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		actor, _, ok := s.requireGlobalPermission(w, r, "Templates.Manage", true)
		if !ok {
			return
		}
		data, ok := decodeEggInput(w, r)
		if !ok {
			return
		}
		if path == "analyze/egg" {
			template, err := s.templates.Analyze(data)
			if err != nil {
				errorOut(w, http.StatusUnprocessableEntity, "invalid_egg", "egg could not be safely normalized")
				return
			}
			jsonOut(w, http.StatusOK, template)
			return
		}
		template, err := s.templates.Import(r.Context(), data)
		if err != nil {
			s.recordTemplateAudit(r, actor, audit.TemplateImport, audit.Failure, templates.Template{}, err)
			if errors.Is(err, templates.ErrInvalidEgg) {
				errorOut(w, http.StatusUnprocessableEntity, "invalid_egg", "egg could not be safely imported")
			} else {
				internal(w)
			}
			return
		}
		s.recordTemplateAudit(r, actor, audit.TemplateImport, audit.Success, template, nil)
		jsonOut(w, http.StatusCreated, template)
		return
	}
	if path == "" || strings.Contains(path, "/") {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Templates.View", false); !ok {
			return
		}
		template, err := s.templates.Get(r.Context(), path)
		if errors.Is(err, sql.ErrNoRows) {
			notFound(w)
			return
		}
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, template)
	case http.MethodDelete:
		actor, _, ok := s.requireGlobalPermission(w, r, "Templates.Manage", true)
		if !ok {
			return
		}
		template, err := s.templates.Get(r.Context(), path)
		if errors.Is(err, sql.ErrNoRows) {
			notFound(w)
			return
		}
		if err != nil {
			internal(w)
			return
		}
		if err = s.templates.Delete(r.Context(), path); err != nil {
			s.recordTemplateAudit(r, actor, audit.TemplateDelete, audit.Failure, template, err)
			if errors.Is(err, templates.ErrBuiltinReadOnly) {
				errorOut(w, http.StatusConflict, "read_only_template", "official and built-in templates cannot be deleted")
				return
			}
			internal(w)
			return
		}
		s.recordTemplateAudit(r, actor, audit.TemplateDelete, audit.Success, template, nil)
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}
