package api

import (
	"encoding/json"
	"net/http"

	"gamenode/internal/audit"
	"gamenode/internal/settings"
)

func (s *Server) settingsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Settings.View", false); !ok {
			return
		}
		values, err := s.settings.Get(r.Context())
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, values)
	case http.MethodPatch:
		actor, _, ok := s.requireGlobalPermission(w, r, "Settings.Manage", true)
		if !ok {
			return
		}
		var patch settings.Patch
		if !decode(w, r, &patch) {
			return
		}
		values, changed, err := s.settings.Update(r.Context(), patch)
		if err != nil {
			code, summary := auditFailure(err)
			s.recordAudit(r, auditInput{action: audit.SettingsUpdate, resourceType: audit.Settings, result: audit.Failure, actor: &actor, errorCode: code, errorSummary: summary})
			bad(w, err.Error())
			return
		}
		metadata, _ := json.Marshal(map[string]any{"changed_fields": changed})
		s.recordAudit(r, auditInput{action: audit.SettingsUpdate, resourceType: audit.Settings, result: audit.Success, actor: &actor, metadata: metadata})
		jsonOut(w, http.StatusOK, values)
	default:
		method(w)
	}
}
