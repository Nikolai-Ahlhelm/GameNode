package api

import (
	"encoding/json"
	"net/http"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/minecraft"
)

func (s *Server) minecraftMessagesHandler(w http.ResponseWriter, r *http.Request, id string) {
	if s.minecraftMessages == nil {
		notFound(w)
		return
	}
	if r.Method == http.MethodGet {
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
		value, err := s.minecraftMessages.Get(r.Context(), id)
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"settings": value})
		return
	}
	if r.Method != http.MethodPut {
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
	var value minecraft.MessageSettings
	if !decode(w, r, &value) {
		return
	}
	if err = s.minecraftMessages.Save(r.Context(), id, value); err != nil {
		bad(w, err.Error())
		s.recordMinecraftMessagesAudit(r, actor, id, value, err)
		return
	}
	s.recordMinecraftMessagesAudit(r, actor, id, value, nil)
	jsonOut(w, http.StatusOK, map[string]any{"settings": value})
}

func (s *Server) recordMinecraftMessagesAudit(r *http.Request, actor auth.User, id string, value minecraft.MessageSettings, err error) {
	metadata, _ := json.Marshal(map[string]any{"restart_warning_count": len(value.RestartWarnings), "motd_count": len(value.MOTDs)})
	in := auditInput{action: audit.ServerUpdate, resourceType: audit.Server, resourceID: stringPointer(id), serverID: stringPointer(id), result: audit.Success, actor: &actor, metadata: metadata}
	if err != nil {
		in.result = audit.Failure
		in.errorCode = "invalid_minecraft_messages"
		in.errorSummary = "Minecraft messages could not be saved"
		in.err = err
	}
	s.recordAudit(r, in)
}
