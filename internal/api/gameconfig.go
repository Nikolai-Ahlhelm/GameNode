package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"gamenode/internal/audit"
	"gamenode/internal/gameconfig"
)

type gameConfigurationInput struct {
	AdapterID string            `json:"adapter_id"`
	Values    map[string]string `json:"values"`
}

func (s *Server) gameConfigurationHandler(w http.ResponseWriter, r *http.Request, serverID string) {
	permission, csrfRequired := "Server.View", false
	if r.Method == http.MethodPut {
		permission, csrfRequired = "Server.Edit", true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		method(w)
		return
	}
	actor, _, ok := s.requireServerPermission(w, r, permission, serverID, csrfRequired)
	if !ok {
		return
	}
	if s.gameConfig == nil {
		jsonOut(w, http.StatusOK, gameconfig.Result{Available: false, Adapters: []gameconfig.AdapterView{}})
		return
	}
	if r.Method == http.MethodGet {
		result, err := s.gameConfig.Get(r.Context(), serverID)
		if err != nil {
			gameConfigurationError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, result)
		return
	}
	var input gameConfigurationInput
	if !decode(w, r, &input) {
		return
	}
	if input.AdapterID == "" || len(input.Values) == 0 || len(input.Values) > 128 {
		bad(w, "invalid configuration update")
		return
	}
	result, err := s.gameConfig.Update(r.Context(), serverID, input.AdapterID, input.Values)
	if err != nil {
		s.recordServerAudit(r, actor, "server.config_update", audit.Failure, serverID, "", err)
		gameConfigurationError(w, err)
		return
	}
	metadata, _ := json.Marshal(map[string]any{"adapter_id": input.AdapterID, "field_count": len(input.Values), "restart_required": true})
	id := serverID
	s.recordAudit(r, auditInput{action: "server.config_update", resourceType: audit.Server, resourceID: &id, serverID: &id, result: audit.Success, metadata: metadata, actor: &actor})
	jsonOut(w, http.StatusOK, result)
}

func gameConfigurationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gameconfig.ErrUnavailable):
		errorOut(w, http.StatusNotFound, "configuration_unavailable", "Managed game configuration is not available for this server")
	case errors.Is(err, gameconfig.ErrInvalidValue), errors.Is(err, gameconfig.ErrUnsafeTarget):
		errorOut(w, http.StatusBadRequest, "invalid_configuration", "Game configuration values are invalid")
	default:
		errorOut(w, http.StatusConflict, "configuration_write_failed", "Game configuration could not be read or updated safely")
	}
}
