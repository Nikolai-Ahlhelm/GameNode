package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/minecraft"
	"gamenode/internal/servers"
)

type minecraftStartupResponse struct {
	Configuration    minecraft.StartupConfiguration `json:"configuration"`
	Executable       string                         `json:"executable"`
	Arguments        []string                       `json:"arguments"`
	WorkingDirectory string                         `json:"working_directory"`
	JavaFound        bool                           `json:"java_found"`
	Container        bool                           `json:"container"`
}

func (s *Server) minecraftStartupHandler(w http.ResponseWriter, r *http.Request, id string, importing bool) {
	if s.minecraftStartup == nil {
		notFound(w)
		return
	}
	if r.Method == http.MethodGet && !importing {
		if _, _, ok := s.requireServerPermission(w, r, "Server.View", id, false); !ok {
			return
		}
		record, err := s.servers.Get(r.Context(), id)
		if err != nil {
			serverError(w, err, false)
			return
		}
		if !s.isMinecraftServer(r, record) {
			notFound(w)
			return
		}
		config, err := s.minecraftStartup.Get(r.Context(), id)
		if err != nil {
			internal(w)
			return
		}
		launch, err := resolveMinecraftStartup(record, config)
		if err != nil {
			errorOut(w, http.StatusUnprocessableEntity, "minecraft_startup_invalid", err.Error())
			return
		}
		jsonOut(w, http.StatusOK, minecraftStartupResponse{Configuration: config, Executable: launch.Executable, Arguments: launch.Arguments, WorkingDirectory: launch.WorkingDirectory, JavaFound: launch.JavaFound, Container: record.Server.RuntimeType == servers.RuntimeContainer})
		return
	}
	if importing {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		actor, _, ok := s.requireServerPermission(w, r, "Server.Edit", id, true)
		if !ok {
			return
		}
		record, err := s.servers.Get(r.Context(), id)
		if err != nil {
			serverError(w, err, false)
			return
		}
		if !s.isMinecraftServer(r, record) {
			notFound(w)
			return
		}
		content, err := s.files.ReadFile(record.Server.WorkingDirectory, "user_jvm_args.txt")
		if err != nil {
			errorOut(w, http.StatusNotFound, "jvm_arguments_unavailable", "user_jvm_args.txt could not be read safely")
			return
		}
		imported, err := minecraft.ImportJVMArguments([]byte(content.Content))
		if err != nil {
			bad(w, err.Error())
			return
		}
		current, err := s.minecraftStartup.Get(r.Context(), id)
		if err != nil {
			internal(w)
			return
		}
		current.XmsMB, current.XmxMB = imported.XmsMB, imported.XmxMB
		current.JVMArguments = imported.JVMArguments
		if err = s.applyMinecraftStartup(r, id, current); err != nil {
			s.recordMinecraftStartupAudit(r, actor, id, current, err)
			minecraftStartupError(w, err)
			return
		}
		s.recordMinecraftStartupAudit(r, actor, id, current, nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	actor, _, ok := s.requireServerPermission(w, r, "Server.Edit", id, true)
	if !ok {
		return
	}
	var config minecraft.StartupConfiguration
	if !decode(w, r, &config) {
		return
	}
	if err := s.applyMinecraftStartup(r, id, config); err != nil {
		s.recordMinecraftStartupAudit(r, actor, id, config, err)
		minecraftStartupError(w, err)
		return
	}
	s.recordMinecraftStartupAudit(r, actor, id, config, nil)
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	config, err = s.minecraftStartup.Get(r.Context(), id)
	if err != nil {
		internal(w)
		return
	}
	launch, err := resolveMinecraftStartup(record, config)
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, minecraftStartupResponse{Configuration: config, Executable: launch.Executable, Arguments: launch.Arguments, WorkingDirectory: launch.WorkingDirectory, JavaFound: launch.JavaFound, Container: record.Server.RuntimeType == servers.RuntimeContainer})
}

func (s *Server) recordMinecraftStartupAudit(r *http.Request, actor auth.User, serverID string, config minecraft.StartupConfiguration, err error) {
	metadata, _ := json.Marshal(map[string]any{"distribution": config.Distribution, "xms_mb": config.XmsMB, "xmx_mb": config.XmxMB, "jvm_argument_count": len(config.JVMArguments), "program_argument_count": len(config.ProgramArguments)})
	input := auditInput{action: audit.ServerUpdate, resourceType: audit.Server, resourceID: stringPointer(serverID), serverID: stringPointer(serverID), result: audit.Success, actor: &actor, metadata: metadata}
	if err != nil {
		input.result = audit.Failure
		input.errorCode, input.errorSummary = auditFailure(err)
		input.err = err
	}
	s.recordAudit(r, input)
}

func (s *Server) applyMinecraftStartup(r *http.Request, id string, config minecraft.StartupConfiguration) error {
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		return err
	}
	if !s.isMinecraftServer(r, record) {
		return errors.New("Minecraft startup settings are unavailable for this server")
	}
	if record.Runtime.CurrentState == servers.StateRunning || record.Runtime.CurrentState == servers.StateStarting || record.Runtime.CurrentState == servers.StateStopping {
		return errors.New("stop the server before editing Minecraft startup settings")
	}
	launch, err := resolveMinecraftStartup(record, config)
	if err != nil {
		return err
	}
	// Persist the operator's typed settings and the resolved structured process
	// argv on the ordinary server record. Native and Container continue through
	// servers.Service and their existing runtime implementation.
	server := record.Server
	if server.RuntimeType == servers.RuntimeContainer {
		if server.Container == nil {
			return errors.New("container configuration is unavailable")
		}
		server.Container.Command = append([]string{launch.Executable}, launch.Arguments...)
		server.Container.StartupShell, server.Container.StartupTemplate = "", ""
	} else {
		server.Executable, server.Arguments = launch.Executable, launch.Arguments
	}
	server.StopMethod, server.StopCommand, server.StopTimeoutSeconds = "stdin_command", "stop", 60
	_, err = s.servers.Update(r.Context(), id, server)
	if err != nil {
		return err
	}
	return s.minecraftStartup.Save(r.Context(), id, config)
}

func (s *Server) isMinecraftServer(r *http.Request, record servers.Record) bool {
	id, _ := s.servers.TemplateID(r.Context(), record.Server.ID)
	if strings.Contains(strings.ToLower(id), "minecraft") || strings.Contains(strings.ToLower(id), "neoforge") {
		return true
	}
	_, err := s.files.Stat(record.Server.WorkingDirectory, "server.jar")
	return err == nil
}

func resolveMinecraftStartup(record servers.Record, config minecraft.StartupConfiguration) (minecraft.Launch, error) {
	limit := int64(0)
	platform := ""
	if record.Server.RuntimeType == servers.RuntimeContainer {
		if record.Server.Container == nil {
			return minecraft.Launch{}, errors.New("container configuration is unavailable")
		}
		limit = record.Server.Container.MemoryLimitBytes
		if config.JavaExecutable == "" {
			config.JavaExecutable = "java"
		}
		platform = "linux"
	}
	return minecraft.ResolveConfiguredLaunch(record.Server.WorkingDirectory, platform, config, limit, record.Server.RuntimeType == servers.RuntimeContainer)
}

func minecraftStartupError(w http.ResponseWriter, err error) {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "Minecraft startup configuration could not be saved"
	}
	if strings.Contains(message, "stop the server") {
		errorOut(w, http.StatusConflict, "server_running", message)
		return
	}
	bad(w, message)
}
