package api

import (
	"net/http"

	"gamenode/internal/rbac"
)

func (s *Server) monitoringHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requirePermission(w, r, "Monitoring.View", rbac.Scope{Type: "server", ID: &id}, false); !ok {
		return
	}
	current, err := s.servers.MonitoringSnapshot(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	jsonOut(w, http.StatusOK, current)
}
func (s *Server) monitoringHistoryHandler(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requirePermission(w, r, "Monitoring.View", rbac.Scope{Type: "server", ID: &id}, false); !ok {
		return
	}
	history, err := s.servers.MonitoringHistory(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"samples": history})
}
