package api

import "net/http"

func (s *Server) diagnosticsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Settings.View", false); !ok {
		return
	}
	jsonOut(w, http.StatusOK, s.diagnostics.Get(r.Context()))
}
