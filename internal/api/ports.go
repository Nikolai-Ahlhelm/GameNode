package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/ports"
)

func portAuditMetadata(port ports.Port) json.RawMessage {
	metadata, err := json.Marshal(map[string]any{"protocol": port.Protocol, "bind_address": port.BindAddress, "port": port.Port})
	if err != nil {
		return nil
	}
	return metadata
}

func (s *Server) recordPortAudit(r *http.Request, actor auth.User, action, result, server string, port ports.Port, err error) {
	serverID := server
	var resourceID *string
	if port.ID != "" {
		resourceID = &port.ID
	}
	in := auditInput{action: action, resourceType: audit.Port, resourceID: resourceID, resourceName: port.Name, serverID: &serverID, result: result, actor: &actor}
	if result == audit.Success {
		in.metadata = portAuditMetadata(port)
	}
	if err != nil {
		in.errorCode, in.errorSummary = auditFailure(err)
	}
	s.recordAudit(r, in)
}

func (s *Server) portsHandler(w http.ResponseWriter, r *http.Request, server string) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/servers/"+server+"/ports"), "/")
	id := ""
	if len(parts) > 1 {
		id = parts[1]
	}
	manage := r.Method != http.MethodGet
	perm := "Ports.View"
	if manage {
		perm = "Ports.Manage"
	}
	u, _, ok := s.requireServerPermission(w, r, perm, server, manage)
	if !ok {
		return
	}
	if id == "" && r.Method == http.MethodGet {
		x, e := s.ports.List(r.Context(), server)
		if e != nil {
			internal(w)
			return
		}
		jsonOut(w, 200, map[string]any{"ports": x})
		return
	}
	var p ports.Port
	if r.Method == http.MethodPost && id == "" {
		if !decode(w, r, &p) {
			return
		}
		x, e := s.ports.Add(r.Context(), server, p)
		if e != nil {
			s.recordPortAudit(r, u, audit.PortCreate, audit.Failure, server, ports.Port{}, e)
			portError(w, e)
			return
		}
		s.recordPortAudit(r, u, audit.PortCreate, audit.Success, server, x, nil)
		jsonOut(w, 201, map[string]any{"port": x})
		return
	}
	if r.Method == http.MethodPatch && id != "" {
		if !decode(w, r, &p) {
			return
		}
		x, e := s.ports.Update(r.Context(), server, id, p)
		if e == sql.ErrNoRows {
			notFound(w)
			return
		}
		if e != nil {
			s.recordPortAudit(r, u, audit.PortUpdate, audit.Failure, server, ports.Port{ID: id}, e)
			portError(w, e)
			return
		}
		s.recordPortAudit(r, u, audit.PortUpdate, audit.Success, server, x, nil)
		jsonOut(w, 200, map[string]any{"port": x})
		return
	}
	if r.Method == http.MethodDelete && id != "" {
		var existing ports.Port
		if list, err := s.ports.List(r.Context(), server); err == nil {
			for _, candidate := range list {
				if candidate.ID == id {
					existing = candidate
					break
				}
			}
		}
		if e := s.ports.Delete(r.Context(), server, id); e == sql.ErrNoRows {
			notFound(w)
		} else if e != nil {
			s.recordPortAudit(r, u, audit.PortDelete, audit.Failure, server, existing, e)
			internal(w)
		} else {
			existing.ID = id
			s.recordPortAudit(r, u, audit.PortDelete, audit.Success, server, existing, nil)
			w.WriteHeader(204)
		}
		return
	}
	method(w)
}

// portError keeps persistence and host-specific probe errors out of the API
// while preserving useful, stable responses for validation and collisions.
func portError(w http.ResponseWriter, err error) {
	code, summary := auditFailure(err)
	switch code {
	case "port_conflict":
		errorOut(w, http.StatusConflict, code, summary)
	case "invalid_port", "invalid_protocol", "invalid_bind_address":
		errorOut(w, http.StatusBadRequest, code, summary)
	default:
		internal(w)
	}
}
