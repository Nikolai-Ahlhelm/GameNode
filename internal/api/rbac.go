package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/rbac"
)

func (s *Server) recordRoleAudit(r *http.Request, actor auth.User, action, result, id, name string, metadata map[string]any, err error) {
	var resourceID *string
	if id != "" {
		resourceID = &id
	}
	in := auditInput{action: action, resourceType: audit.Role, resourceID: resourceID, resourceName: name, result: result, actor: &actor}
	if metadata != nil && result == audit.Success {
		in.metadata, _ = json.Marshal(metadata)
	}
	if err != nil {
		in.errorCode, in.errorSummary = auditFailure(err)
		in.err = err
	}
	s.recordAudit(r, in)
}

func (s *Server) permissionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
		return
	}
	permissions := make([]map[string]any, 0, len(rbac.Catalog))
	for _, permission := range rbac.Catalog {
		permissions = append(permissions, map[string]any{"key": permission.Key, "category": permission.Category, "description": permission.Description, "allowed_scopes": rbac.AllowedScopes(permission.Key)})
	}
	jsonOut(w, http.StatusOK, map[string]any{"permissions": permissions})
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
		actor, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true)
		if !ok {
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
			s.recordRoleAudit(r, actor, audit.RoleCreate, audit.Failure, "", "", nil, e)
			rbacError(w, e)
			return
		}
		s.recordRoleAudit(r, actor, audit.RoleCreate, audit.Success, x.ID, x.Name, nil, nil)
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
			actor, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true)
			if !ok {
				return
			}
			var in struct {
				Permissions []string `json:"permissions"`
			}
			if !decode(w, r, &in) {
				return
			}
			if e := s.rbac.ReplacePermissions(r.Context(), id, in.Permissions); e != nil {
				s.recordRoleAudit(r, actor, audit.RolePermissionsUpdate, audit.Failure, id, "", nil, e)
				rbacError(w, e)
				return
			}
			name := ""
			if role, err := s.rbac.GetRole(r.Context(), id); err == nil {
				name = role.Name
			}
			metadata := map[string]any{"permission_count": len(in.Permissions)}
			encoded, _ := json.Marshal(map[string]any{"permission_count": len(in.Permissions), "permissions": in.Permissions})
			if len(encoded) <= audit.MaxMetadataBytes {
				metadata["permissions"] = in.Permissions
			}
			s.recordRoleAudit(r, actor, audit.RolePermissionsUpdate, audit.Success, id, name, metadata, nil)
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
		actor, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true)
		if !ok {
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
			s.recordRoleAudit(r, actor, audit.RoleUpdate, audit.Failure, id, "", nil, e)
			rbacError(w, e)
			return
		}
		s.recordRoleAudit(r, actor, audit.RoleUpdate, audit.Success, x.ID, x.Name, nil, nil)
		jsonOut(w, http.StatusOK, map[string]any{"role": x})
	case http.MethodDelete:
		actor, _, ok := s.requireGlobalPermission(w, r, "Roles.Manage", true)
		if !ok {
			return
		}
		name := ""
		if role, err := s.rbac.GetRole(r.Context(), id); err == nil {
			name = role.Name
		}
		if e := s.rbac.DeleteRole(r.Context(), id); e != nil {
			s.recordRoleAudit(r, actor, audit.RoleDelete, audit.Failure, id, name, nil, e)
			rbacError(w, e)
			return
		}
		s.recordRoleAudit(r, actor, audit.RoleDelete, audit.Success, id, name, nil, nil)
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
	if errors.Is(e, rbac.ErrInvalidScope) {
		bad(w, "Role contains permissions that cannot be assigned at server scope.")
		return
	}
	if errors.Is(e, rbac.ErrEmptyServerRole) {
		bad(w, "Role has no permissions and cannot be assigned at server scope.")
		return
	}
	if errors.Is(e, rbac.ErrRoleHasServerAssignments) {
		bad(w, "Remove the role's server assignments before making the role empty or adding global-only permissions.")
		return
	}
	if errors.Is(e, rbac.ErrInvalidTenantScope) {
		bad(w, "Role contains permissions that cannot be assigned at tenant scope.")
		return
	}
	if errors.Is(e, rbac.ErrEmptyTenantRole) {
		bad(w, "Role has no permissions and cannot be assigned at tenant scope.")
		return
	}
	if errors.Is(e, rbac.ErrRoleHasTenantAssignments) {
		bad(w, "Remove the role's tenant assignments before making the role empty or adding permissions that do not support tenant scope.")
		return
	}
	if errors.Is(e, rbac.ErrBuiltinRoleProtected) {
		errorOut(w, http.StatusConflict, "builtin_role", "built-in roles cannot be deleted; adjust their name, description, or permissions instead")
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
			var in struct {
				RoleID    string  `json:"role_id"`
				ScopeType string  `json:"scope_type"`
				ScopeID   *string `json:"scope_id"`
			}
			if !decode(w, r, &in) {
				return
			}
			actor, _, ok := s.requireAssignmentManage(w, r, rbac.Scope{Type: in.ScopeType, ID: in.ScopeID})
			if !ok {
				return
			}
			if e := s.rbac.AssignUser(r.Context(), user, in.RoleID, rbac.Scope{Type: in.ScopeType, ID: in.ScopeID}); e != nil {
				s.recordRoleAudit(r, actor, audit.RoleAssignmentAdd, audit.Failure, in.RoleID, "", nil, e)
				rbacError(w, e)
				return
			}
			x, e := s.rbac.ListUserAssignments(r.Context(), user)
			if e != nil {
				rbacError(w, e)
				return
			}
			roleName := ""
			if role, err := s.rbac.GetRole(r.Context(), in.RoleID); err == nil {
				roleName = role.Name
			}
			metadata := map[string]any{"subject_type": "user", "subject_id": user, "scope": in.ScopeType}
			if in.ScopeID != nil {
				metadata["scope_id"] = *in.ScopeID
			}
			s.recordRoleAudit(r, actor, audit.RoleAssignmentAdd, audit.Success, in.RoleID, roleName, metadata, nil)
			assignment, found := matchingAssignment(x, in.RoleID, rbac.Scope{Type: in.ScopeType, ID: in.ScopeID})
			if !found {
				internal(w)
				return
			}
			jsonOut(w, http.StatusCreated, map[string]any{"assignment": assignment})
		default:
			method(w)
		}
		return
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		var assignment rbac.Assignment
		if list, err := s.rbac.ListUserAssignments(r.Context(), user); err == nil {
			for _, candidate := range list {
				if candidate.ID == parts[2] {
					assignment = candidate
					break
				}
			}
		}
		actor, _, ok := s.requireAssignmentManage(w, r, assignment.Scope)
		if !ok {
			return
		}
		if e := s.rbac.RemoveUserAssignmentFor(r.Context(), user, parts[2]); e != nil {
			s.recordRoleAudit(r, actor, audit.RoleAssignmentRemove, audit.Failure, assignment.RoleID, assignment.RoleName, nil, e)
			rbacError(w, e)
			return
		}
		metadata := map[string]any{"subject_type": "user", "subject_id": user, "scope": assignment.Scope.Type}
		if assignment.Scope.ID != nil {
			metadata["scope_id"] = *assignment.Scope.ID
		}
		s.recordRoleAudit(r, actor, audit.RoleAssignmentRemove, audit.Success, assignment.RoleID, assignment.RoleName, metadata, nil)
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
			var in struct {
				RoleID    string  `json:"role_id"`
				ScopeType string  `json:"scope_type"`
				ScopeID   *string `json:"scope_id"`
			}
			if !decode(w, r, &in) {
				return
			}
			actor, _, ok := s.requireAssignmentManage(w, r, rbac.Scope{Type: in.ScopeType, ID: in.ScopeID})
			if !ok {
				return
			}
			if e := s.rbac.AssignGroup(r.Context(), group, in.RoleID, rbac.Scope{Type: in.ScopeType, ID: in.ScopeID}); e != nil {
				s.recordRoleAudit(r, actor, audit.RoleAssignmentAdd, audit.Failure, in.RoleID, "", nil, e)
				rbacError(w, e)
				return
			}
			x, e := s.rbac.ListGroupAssignments(r.Context(), group)
			if e != nil {
				rbacError(w, e)
				return
			}
			roleName := ""
			if role, err := s.rbac.GetRole(r.Context(), in.RoleID); err == nil {
				roleName = role.Name
			}
			metadata := map[string]any{"subject_type": "group", "subject_id": group, "scope": in.ScopeType}
			if in.ScopeID != nil {
				metadata["scope_id"] = *in.ScopeID
			}
			s.recordRoleAudit(r, actor, audit.RoleAssignmentAdd, audit.Success, in.RoleID, roleName, metadata, nil)
			assignment, found := matchingAssignment(x, in.RoleID, rbac.Scope{Type: in.ScopeType, ID: in.ScopeID})
			if !found {
				internal(w)
				return
			}
			jsonOut(w, http.StatusCreated, map[string]any{"assignment": assignment})
		default:
			method(w)
		}
		return
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		var assignment rbac.Assignment
		if list, err := s.rbac.ListGroupAssignments(r.Context(), group); err == nil {
			for _, candidate := range list {
				if candidate.ID == parts[2] {
					assignment = candidate
					break
				}
			}
		}
		actor, _, ok := s.requireAssignmentManage(w, r, assignment.Scope)
		if !ok {
			return
		}
		if e := s.rbac.RemoveGroupAssignmentFor(r.Context(), group, parts[2]); e != nil {
			s.recordRoleAudit(r, actor, audit.RoleAssignmentRemove, audit.Failure, assignment.RoleID, assignment.RoleName, nil, e)
			rbacError(w, e)
			return
		}
		metadata := map[string]any{"subject_type": "group", "subject_id": group, "scope": assignment.Scope.Type}
		if assignment.Scope.ID != nil {
			metadata["scope_id"] = *assignment.Scope.ID
		}
		s.recordRoleAudit(r, actor, audit.RoleAssignmentRemove, audit.Success, assignment.RoleID, assignment.RoleName, metadata, nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	method(w)
}

func matchingAssignment(assignments []rbac.Assignment, roleID string, scope rbac.Scope) (rbac.Assignment, bool) {
	for _, assignment := range assignments {
		if assignment.RoleID != roleID || assignment.Scope.Type != scope.Type {
			continue
		}
		if scope.ID == nil && assignment.Scope.ID == nil {
			return assignment, true
		}
		if scope.ID != nil && assignment.Scope.ID != nil && *scope.ID == *assignment.Scope.ID {
			return assignment, true
		}
	}
	return rbac.Assignment{}, false
}
