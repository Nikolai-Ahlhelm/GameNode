package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/identity"
	"gamenode/internal/rbac"
)

func (s *Server) recordIdentityAudit(r *http.Request, actor auth.User, action, resourceType, id, name, result string, metadata map[string]any, err error) {
	var resourceID *string
	if id != "" {
		resourceID = &id
	}
	in := auditInput{action: action, resourceType: resourceType, resourceID: resourceID, resourceName: name, result: result, actor: &actor}
	if metadata != nil && result == audit.Success {
		in.metadata, _ = json.Marshal(metadata)
	}
	if err != nil {
		in.errorCode, in.errorSummary = auditFailure(err)
		if errors.Is(err, identity.ErrLastActiveAdmin) {
			in.errorCode, in.errorSummary = "last_active_admin", "last active administrator protection"
		}
	}
	s.recordAudit(r, in)
}

func (s *Server) usersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		actor, _, ok := s.requireGlobalPermission(w, r, "Users.View", false)
		if !ok {
			return
		}
		users, err := s.identity.ListUserSummaries(r.Context())
		if err != nil {
			internal(w)
			return
		}
		groupMembershipsVisible, err := s.allowed(r.Context(), actor, "Groups.View", rbac.Scope{Type: "global"})
		if err != nil {
			internal(w)
			return
		}
		if !groupMembershipsVisible {
			for i := range users {
				users[i].GroupCount = nil
			}
		}
		jsonOut(w, http.StatusOK, map[string]any{"users": users})
	case http.MethodPost:
		actor, _, ok := s.requireGlobalPermission(w, r, "Users.Manage", true)
		if !ok {
			return
		}
		var in identity.CreateUserInput
		if !decode(w, r, &in) {
			return
		}
		if in.IsAdmin && !actor.IsAdmin {
			forbidden(w, "administrator access required to set administrator flag")
			return
		}
		user, err := s.identity.CreateUser(r.Context(), in)
		if err != nil {
			s.recordIdentityAudit(r, actor, audit.UserCreate, audit.User, "", "", audit.Failure, nil, err)
			identityError(w, err)
			return
		}
		s.recordIdentityAudit(r, actor, audit.UserCreate, audit.User, user.ID, user.Username, audit.Success, map[string]any{"enabled": user.Enabled}, nil)
		s.log.With("module", "Identity.UserCreate").Info("user created", "user_id", user.ID)
		jsonOut(w, http.StatusCreated, map[string]any{"user": user})
	default:
		method(w)
	}
}
func (s *Server) userHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/users/")
	parts := strings.Split(path, "/")
	if parts[0] == "" || len(parts) > 3 {
		notFound(w)
		return
	}
	id := parts[0]
	if len(parts) >= 2 && parts[1] == "roles" {
		s.userRolesHandler(w, r, id, parts)
		return
	}
	if len(parts) == 2 && parts[1] == "groups" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		if _, _, ok := s.requireGlobalPermission(w, r, "Users.View", false); !ok {
			return
		}
		if _, _, ok := s.requireGlobalPermission(w, r, "Groups.View", false); !ok {
			return
		}
		groups, err := s.identity.GroupsForUser(r.Context(), id)
		if err != nil {
			identityError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"groups": groups})
		return
	}
	if len(parts) == 2 && parts[1] == "password" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		actor, _, ok := s.requireGlobalPermission(w, r, "Users.Manage", true)
		if !ok {
			return
		}
		var in struct {
			Password string `json:"password"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := s.identity.ResetPassword(r.Context(), id, in.Password); err != nil {
			s.recordIdentityAudit(r, actor, audit.UserPasswordReset, audit.User, id, "", audit.Failure, nil, err)
			identityError(w, err)
			return
		}
		name := ""
		if target, err := s.identity.GetUser(r.Context(), id); err == nil {
			name = target.Username
		}
		s.recordIdentityAudit(r, actor, audit.UserPasswordReset, audit.User, id, name, audit.Success, map[string]any{"sessions_invalidated": true}, nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) != 1 {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Users.View", false); !ok {
			return
		}
		user, err := s.identity.GetUser(r.Context(), id)
		if err != nil {
			identityError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"user": user})
	case http.MethodPatch:
		actor, _, ok := s.requireGlobalPermission(w, r, "Users.Manage", true)
		if !ok {
			return
		}
		var in identity.UpdateUserInput
		if !decode(w, r, &in) {
			return
		}
		if in.IsAdmin != nil && !actor.IsAdmin {
			s.recordIdentityAudit(r, actor, audit.UserUpdate, audit.User, id, "", audit.Failure, nil, errors.New("administrator flag change denied"))
			forbidden(w, "administrator access required to change administrator flag")
			return
		}
		user, err := s.identity.UpdateUser(r.Context(), actor.ID, id, in)
		if err != nil {
			action := audit.UserUpdate
			if in.Enabled != nil {
				if *in.Enabled {
					action = audit.UserEnable
				} else {
					action = audit.UserDisable
				}
			}
			s.recordIdentityAudit(r, actor, action, audit.User, id, "", audit.Failure, nil, err)
			identityError(w, err)
			return
		}
		action := audit.UserUpdate
		if in.Enabled != nil {
			if *in.Enabled {
				action = audit.UserEnable
			} else {
				action = audit.UserDisable
			}
		}
		s.recordIdentityAudit(r, actor, action, audit.User, user.ID, user.Username, audit.Success, nil, nil)
		jsonOut(w, http.StatusOK, map[string]any{"user": user})
	case http.MethodDelete:
		actor, _, ok := s.requireGlobalPermission(w, r, "Users.Manage", true)
		if !ok {
			return
		}
		name := ""
		if target, err := s.identity.GetUser(r.Context(), id); err == nil {
			name = target.Username
		}
		if err := s.identity.DeleteUser(r.Context(), actor.ID, id); err != nil {
			s.recordIdentityAudit(r, actor, audit.UserDelete, audit.User, id, name, audit.Failure, nil, err)
			identityError(w, err)
			return
		}
		s.recordIdentityAudit(r, actor, audit.UserDelete, audit.User, id, name, audit.Success, map[string]any{"sessions_invalidated": true}, nil)
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}
func (s *Server) groupsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		actor, _, ok := s.requireGlobalPermission(w, r, "Groups.View", false)
		if !ok {
			return
		}
		groups, err := s.identity.ListGroupSummaries(r.Context())
		if err != nil {
			internal(w)
			return
		}
		roleAssignmentsVisible, err := s.allowed(r.Context(), actor, "Roles.View", rbac.Scope{Type: "global"})
		if err != nil {
			internal(w)
			return
		}
		if !roleAssignmentsVisible {
			for i := range groups {
				groups[i].AssignmentCount = nil
			}
		}
		jsonOut(w, http.StatusOK, map[string]any{"groups": groups})
	case http.MethodPost:
		actor, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true)
		if !ok {
			return
		}
		var in identity.CreateGroupInput
		if !decode(w, r, &in) {
			return
		}
		group, err := s.identity.CreateGroup(r.Context(), in)
		if err != nil {
			s.recordIdentityAudit(r, actor, audit.GroupCreate, audit.Group, "", "", audit.Failure, nil, err)
			identityError(w, err)
			return
		}
		s.recordIdentityAudit(r, actor, audit.GroupCreate, audit.Group, group.ID, group.Name, audit.Success, nil, nil)
		jsonOut(w, http.StatusCreated, map[string]any{"group": group})
	default:
		method(w)
	}
}
func (s *Server) groupHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/groups/")
	parts := strings.Split(path, "/")
	if parts[0] == "" || len(parts) > 3 {
		notFound(w)
		return
	}
	id := parts[0]
	if len(parts) >= 2 && parts[1] == "roles" {
		s.groupRolesHandler(w, r, id, parts)
		return
	}
	if len(parts) == 2 && parts[1] == "members" {
		switch r.Method {
		case http.MethodGet:
			if _, _, ok := s.requireGlobalPermission(w, r, "Groups.View", false); !ok {
				return
			}
			users, err := s.identity.Members(r.Context(), id)
			if err != nil {
				identityError(w, err)
				return
			}
			jsonOut(w, http.StatusOK, map[string]any{"users": users})
		case http.MethodPost:
			actor, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true)
			if !ok {
				return
			}
			var in struct {
				UserID string `json:"user_id"`
			}
			if !decode(w, r, &in) {
				return
			}
			if err := s.identity.AddMember(r.Context(), id, in.UserID); err != nil {
				s.recordIdentityAudit(r, actor, audit.GroupMemberAdd, audit.Group, id, "", audit.Failure, nil, err)
				identityError(w, err)
				return
			}
			metadata := map[string]any{"user_id": in.UserID}
			if target, err := s.identity.GetUser(r.Context(), in.UserID); err == nil {
				metadata["username"] = target.Username
			}
			name := ""
			if group, err := s.identity.GetGroup(r.Context(), id); err == nil {
				name = group.Name
			}
			s.recordIdentityAudit(r, actor, audit.GroupMemberAdd, audit.Group, id, name, audit.Success, metadata, nil)
			w.WriteHeader(http.StatusNoContent)
		default:
			method(w)
		}
		return
	}
	if len(parts) == 3 && parts[1] == "members" {
		if r.Method != http.MethodDelete {
			method(w)
			return
		}
		actor, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true)
		if !ok {
			return
		}
		if err := s.identity.RemoveMember(r.Context(), id, parts[2]); err != nil {
			s.recordIdentityAudit(r, actor, audit.GroupMemberRemove, audit.Group, id, "", audit.Failure, nil, err)
			identityError(w, err)
			return
		}
		s.recordIdentityAudit(r, actor, audit.GroupMemberRemove, audit.Group, id, "", audit.Success, map[string]any{"user_id": parts[2]}, nil)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) != 1 {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Groups.View", false); !ok {
			return
		}
		g, err := s.identity.GetGroup(r.Context(), id)
		if err != nil {
			identityError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"group": g})
	case http.MethodPatch:
		actor, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true)
		if !ok {
			return
		}
		var in identity.UpdateGroupInput
		if !decode(w, r, &in) {
			return
		}
		g, err := s.identity.UpdateGroup(r.Context(), id, in)
		if err != nil {
			s.recordIdentityAudit(r, actor, audit.GroupUpdate, audit.Group, id, "", audit.Failure, nil, err)
			identityError(w, err)
			return
		}
		s.recordIdentityAudit(r, actor, audit.GroupUpdate, audit.Group, g.ID, g.Name, audit.Success, nil, nil)
		jsonOut(w, http.StatusOK, map[string]any{"group": g})
	case http.MethodDelete:
		actor, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true)
		if !ok {
			return
		}
		name := ""
		if group, err := s.identity.GetGroup(r.Context(), id); err == nil {
			name = group.Name
		}
		if err := s.identity.DeleteGroup(r.Context(), id); err != nil {
			s.recordIdentityAudit(r, actor, audit.GroupDelete, audit.Group, id, name, audit.Failure, nil, err)
			identityError(w, err)
			return
		}
		s.recordIdentityAudit(r, actor, audit.GroupDelete, audit.Group, id, name, audit.Success, nil, nil)
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}
func identityError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w)
		return
	}
	if errors.Is(err, identity.ErrLastActiveAdmin) {
		errorOut(w, http.StatusConflict, "last_active_admin", "At least one active administrator must remain.")
		return
	}
	if errors.Is(err, identity.ErrDuplicateUsername) || errors.Is(err, identity.ErrDuplicateEmail) || errors.Is(err, identity.ErrDuplicateGroup) || errors.Is(err, identity.ErrDuplicateMember) {
		errorOut(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "constraint") {
		errorOut(w, http.StatusConflict, "conflict", "The requested identity change conflicts with existing data.")
		return
	}
	message := err.Error()
	if strings.Contains(message, "must be") || strings.Contains(message, "valid email") {
		bad(w, message)
		return
	}
	internal(w)
}
