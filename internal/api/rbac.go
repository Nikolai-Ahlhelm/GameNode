package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/rbac"
)

func (s *Server) permissionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"permissions": rbac.Catalog})
}
func (s *Server) rolesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
			return
		}
		x, e := s.rbac.ListRoles(r.Context())
		if e != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"roles": x})
		return
	}
	if r.Method == http.MethodPost {
		if _, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true); !ok {
			return
		}
		var in struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, e := s.rbac.CreateRole(r.Context(), in.Name, in.Description)
		if e != nil {
			rbacError(w, e)
			return
		}
		jsonOut(w, http.StatusCreated, map[string]any{"role": x})
		return
	}
	method(w)
}
func (s *Server) roleHandler(w http.ResponseWriter, r *http.Request) {
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/roles/"), "/")
	if p[0] == "" || len(p) > 2 {
		notFound(w)
		return
	}
	id := p[0]
	if len(p) == 2 && p[1] == "permissions" {
		if r.Method == http.MethodGet {
			if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
				return
			}
			x, e := s.rbac.GetRolePermissions(r.Context(), id)
			if e != nil {
				rbacError(w, e)
				return
			}
			jsonOut(w, http.StatusOK, map[string]any{"permissions": x})
			return
		}
		if r.Method == http.MethodPut {
			if _, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true); !ok {
				return
			}
			var in struct {
				Permissions []string `json:"permissions"`
			}
			if !decode(w, r, &in) {
				return
			}
			if e := s.rbac.ReplacePermissions(r.Context(), id, in.Permissions); e != nil {
				rbacError(w, e)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		method(w)
		return
	}
	if len(p) != 1 {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
			return
		}
		x, e := s.rbac.GetRole(r.Context(), id)
		if e != nil {
			rbacError(w, e)
			return
		}
		perms, e := s.rbac.GetRolePermissions(r.Context(), id)
		if e != nil {
			rbacError(w, e)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"role": x, "permissions": perms})
	case http.MethodPatch:
		if _, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true); !ok {
			return
		}
		var in struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if !decode(w, r, &in) {
			return
		}
		x, e := s.rbac.UpdateRole(r.Context(), id, in.Name, in.Description)
		if e != nil {
			rbacError(w, e)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"role": x})
	case http.MethodDelete:
		if _, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true); !ok {
			return
		}
		if e := s.rbac.DeleteRole(r.Context(), id); e != nil {
			rbacError(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}
func rbacError(w http.ResponseWriter, e error) {
	if errors.Is(e, sql.ErrNoRows) {
		notFound(w)
		return
	}
	if errors.Is(e, rbac.ErrUnknownPermission) {
		bad(w, "unknown permission")
		return
	}
	if errors.Is(e, rbac.ErrDuplicateAssignment) || strings.Contains(strings.ToLower(e.Error()), "constraint") {
		errorOut(w, http.StatusConflict, "conflict", "conflicting role data")
		return
	}
	bad(w, e.Error())
}
func (s *Server) userRolesHandler(w http.ResponseWriter, r *http.Request, user string, parts []string) {
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
				return
			}
			x, e := s.rbac.ListUserAssignments(r.Context(), user)
			if e != nil {
				rbacError(w, e)
				return
			}
			jsonOut(w, http.StatusOK, map[string]any{"assignments": x})
		case http.MethodPost:
			if _, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true); !ok {
				return
			}
			var in struct {
				RoleID    string  `json:"role_id"`
				ScopeType string  `json:"scope_type"`
				ScopeID   *string `json:"scope_id"`
			}
			if !decode(w, r, &in) {
				return
			}
			if e := s.rbac.AssignUser(r.Context(), user, in.RoleID, rbac.Scope{Type: in.ScopeType, ID: in.ScopeID}); e != nil {
				rbacError(w, e)
				return
			}
			x, e := s.rbac.ListUserAssignments(r.Context(), user)
			if e != nil {
				rbacError(w, e)
				return
			}
			jsonOut(w, http.StatusCreated, map[string]any{"assignment": x[len(x)-1]})
		default:
			method(w)
		}
		return
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		if _, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true); !ok {
			return
		}
		if e := s.rbac.RemoveUserAssignmentFor(r.Context(), user, parts[2]); e != nil {
			rbacError(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	method(w)
}
func (s *Server) groupRolesHandler(w http.ResponseWriter, r *http.Request, group string, parts []string) {
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
				return
			}
			x, e := s.rbac.ListGroupAssignments(r.Context(), group)
			if e != nil {
				rbacError(w, e)
				return
			}
			jsonOut(w, http.StatusOK, map[string]any{"assignments": x})
		case http.MethodPost:
			if _, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true); !ok {
				return
			}
			var in struct {
				RoleID    string  `json:"role_id"`
				ScopeType string  `json:"scope_type"`
				ScopeID   *string `json:"scope_id"`
			}
			if !decode(w, r, &in) {
				return
			}
			if e := s.rbac.AssignGroup(r.Context(), group, in.RoleID, rbac.Scope{Type: in.ScopeType, ID: in.ScopeID}); e != nil {
				rbacError(w, e)
				return
			}
			x, e := s.rbac.ListGroupAssignments(r.Context(), group)
			if e != nil {
				rbacError(w, e)
				return
			}
			jsonOut(w, http.StatusCreated, map[string]any{"assignment": x[len(x)-1]})
		default:
			method(w)
		}
		return
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		if _, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true); !ok {
			return
		}
		if e := s.rbac.RemoveGroupAssignmentFor(r.Context(), group, parts[2]); e != nil {
			rbacError(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	method(w)
}
