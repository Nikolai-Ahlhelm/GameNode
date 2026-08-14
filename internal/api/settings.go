package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"gamenode/internal/audit"
	"gamenode/internal/logging"
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
			s.log.Error("settings update failed", "module", "Settings.Update", "actor_user_id", actor.ID, "error", err)
			code, summary := auditFailure(err)
			s.recordAudit(r, auditInput{action: audit.SettingsUpdate, resourceType: audit.Settings, result: audit.Failure, actor: &actor, errorCode: code, errorSummary: summary})
			bad(w, err.Error())
			return
		}
		metadata, _ := json.Marshal(map[string]any{"changed_fields": changed})
		s.recordAudit(r, auditInput{action: audit.SettingsUpdate, resourceType: audit.Settings, result: audit.Success, actor: &actor, metadata: metadata})
		s.log.Info("settings updated", "module", "Settings.Update", "actor_user_id", actor.ID, "changed_fields", changed)
		jsonOut(w, http.StatusOK, values)
	default:
		method(w)
	}
}

func (s *Server) brandingFaviconHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	contentType, data, err := s.settings.Favicon(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		internal(w)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) settingsFaviconHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		method(w)
		return
	}
	actor, _, ok := s.requireGlobalPermission(w, r, "Settings.Manage", true)
	if !ok {
		return
	}
	var err error
	if r.Method == http.MethodPut {
		r.Body = http.MaxBytesReader(w, r.Body, settings.MaxFaviconBytes+1)
		var data []byte
		data, err = io.ReadAll(r.Body)
		if err == nil {
			_, err = s.settings.SetFavicon(r.Context(), data)
		}
	} else {
		err = s.settings.DeleteFavicon(r.Context())
	}
	if err != nil {
		s.recordAudit(r, auditInput{action: audit.SettingsUpdate, resourceType: audit.Settings, result: audit.Failure, actor: &actor, errorCode: "invalid_favicon", errorSummary: "favicon could not be updated"})
		bad(w, err.Error())
		return
	}
	metadata, _ := json.Marshal(map[string]any{"changed_fields": []string{"branding.favicon"}})
	s.recordAudit(r, auditInput{action: audit.SettingsUpdate, resourceType: audit.Settings, result: audit.Success, actor: &actor, metadata: metadata})
	values, getErr := s.settings.Get(r.Context())
	if getErr != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, values)
}

func (s *Server) clearLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	actor, _, ok := s.requireGlobalPermission(w, r, "Log.FlushDirectory", true)
	if !ok {
		return
	}
	if s.logs == nil {
		internal(w)
		return
	}
	if err := s.logs.Clear(r.Context()); err != nil {
		s.log.With("module", "Settings.Logs").Error("log files could not be cleared", "error", err.Error())
		s.recordAudit(r, auditInput{action: audit.SettingsLogsClear, resourceType: audit.Settings, result: audit.Failure, actor: &actor, errorCode: "operation_failed", errorSummary: "log files could not be cleared"})
		internal(w)
		return
	}
	s.recordAudit(r, auditInput{action: audit.SettingsLogsClear, resourceType: audit.Settings, result: audit.Success, actor: &actor})
	s.log.With("module", "Settings.Logs").Info("log files cleared", "actor_user_id", actor.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) applicationLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Log.Read", false); !ok {
		return
	}
	if s.logs == nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"entries": s.logs.Entries(), "limit": logging.MaxHistoryEntries})
}
