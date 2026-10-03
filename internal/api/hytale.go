package api

import (
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/ports"
	"gamenode/internal/rbac"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

const hytaleTemplateID = "hytale"

type hytaleInput struct {
	ServerName string            `json:"server_name"`
	ServerRoot string            `json:"server_root"`
	Variables  map[string]string `json:"variables"`
}

type hytaleResolution struct {
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"working_directory"`
	Platform         string   `json:"platform"`
	StopMethod       string   `json:"stop_method"`
	StopCommand      string   `json:"stop_command"`
	StopTimeout      int      `json:"stop_timeout_seconds"`
	Ports            []string `json:"ports"`
}

// hytaleTemplateAction adopts an operator-installed Hytale server folder.
// GameNode cannot install Hytale (its server files require an interactive
// account login), so this only validates the folder and registers a direct Java
// launch. The root is an administrator-supplied host path, so - like the other
// adopt flows - it requires global Server.Create, never a tenant-scoped grant.
func (s *Server) hytaleTemplateAction(w http.ResponseWriter, r *http.Request, templateID, action string) {
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
	if err != nil || template.ID != hytaleTemplateID || template.SourceType != templates.SourceOfficial || template.Installer.Type != templates.InstallerExistingFiles {
		notFound(w)
		return
	}
	var input hytaleInput
	if !decode(w, r, &input) {
		return
	}
	values, _, err := templates.ResolveValues(template, input.Variables)
	if err != nil {
		bad(w, "invalid Hytale settings")
		return
	}
	resolved, err := templates.ResolveLaunch(template, runtime.GOOS, values, input.ServerRoot)
	if err != nil {
		errorOut(w, http.StatusUnprocessableEntity, "hytale_resolution_failed", "The Hytale server folder could not be validated safely (it needs Server/HytaleServer.jar, Assets.zip and Java)")
		return
	}
	serverPorts := make([]ports.Port, 0, len(template.Ports))
	summaries := make([]string, 0, len(template.Ports))
	for _, declared := range template.Ports {
		base := declared.Port
		if declared.Variable != "" {
			parsed, convErr := strconv.Atoi(values[declared.Variable])
			if convErr != nil {
				bad(w, "invalid Hytale port")
				return
			}
			base = parsed
		}
		base += declared.Offset
		serverPorts = append(serverPorts, ports.Port{Name: declared.Name, Protocol: declared.Protocol, Port: base})
		summaries = append(summaries, strings.ToUpper(declared.Protocol)+" "+strconv.Itoa(base))
	}
	if action == "resolve" {
		jsonOut(w, http.StatusOK, hytaleResolution{Executable: resolved.Executable, Arguments: resolved.Arguments, WorkingDirectory: resolved.WorkingDirectory, Platform: runtime.GOOS, StopMethod: resolved.StopMethod, StopCommand: resolved.StopCommand, StopTimeout: resolved.StopTimeout, Ports: summaries})
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	metadata := make([]servers.ProvisionedVariable, 0, len(keys))
	for _, key := range keys {
		metadata = append(metadata, servers.ProvisionedVariable{Key: key, Source: template.SourceType, Version: template.Version})
	}
	server := servers.Server{CreationMode: servers.CreationTemplate, Name: strings.TrimSpace(input.ServerName), Description: template.Description, WorkingDirectory: resolved.WorkingDirectory, Executable: resolved.Executable, Arguments: resolved.Arguments, EnvironmentVariables: map[string]string{}, RuntimeType: "native", RestartPolicy: "never", StopMethod: resolved.StopMethod, StopCommand: resolved.StopCommand, StopTimeoutSeconds: resolved.StopTimeout, AutoRestartMaxAttempts: 3, AutoRestartWindowSeconds: 300, AutoRestartDelaySeconds: 5}
	record, err := s.servers.CreateProvisioned(r.Context(), server, template.ID, metadata, serverPorts, nil, nil)
	if err != nil {
		s.recordServerAudit(r, actor, audit.ServerCreate, audit.Failure, "", server.Name, err)
		serverError(w, err, false)
		return
	}
	s.recordServerAudit(r, actor, audit.ServerCreate, audit.Success, record.Server.ID, record.Server.Name, nil)
	jsonOut(w, http.StatusCreated, record)
}
