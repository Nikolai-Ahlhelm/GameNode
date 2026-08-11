package api

import (
	"net/http"
	"strconv"

	"gamenode/internal/audit"
)

func (s *Server) auditHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Audit.View", false); !ok {
		return
	}
	q := r.URL.Query()
	f := audit.Filter{Action: q.Get("action"), ResourceType: q.Get("resource_type"), Result: q.Get("result")}
	for key, target := range map[string]**string{"actor_user_id": &f.ActorUserID, "resource_id": &f.ResourceID, "server_id": &f.ServerID} {
		if value := q.Get(key); value != "" {
			copy := value
			*target = &copy
		}
	}
	if raw := q.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			bad(w, "invalid limit")
			return
		}
		f.Limit = value
	}
	if raw := q.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			bad(w, "invalid offset")
			return
		}
		f.Offset = value
	}
	if f.Result != "" && f.Result != audit.Success && f.Result != audit.Failure {
		bad(w, "invalid result")
		return
	}
	validResource := map[string]bool{audit.Auth: true, audit.Server: true, audit.Port: true, audit.File: true, audit.Console: true, audit.User: true, audit.Group: true, audit.Role: true, audit.Settings: true, audit.System: true}
	if f.ResourceType != "" && !validResource[f.ResourceType] {
		bad(w, "invalid resource type")
		return
	}
	items, err := s.audit.List(r.Context(), f)
	if err != nil {
		internal(w)
		return
	}
	limit := f.Limit
	if limit <= 0 {
		limit = audit.DefaultLimit
	}
	if limit > audit.MaxLimit {
		limit = audit.MaxLimit
	}
	jsonOut(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": f.Offset})
}
