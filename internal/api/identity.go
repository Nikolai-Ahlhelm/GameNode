package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/identity"
)

func (s *Server) usersHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Users.View", false); !ok {
			return
		}
		users, err := s.identity.ListUsers(r.Context())
		if err != nil {
			internal(w)
			return
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
			identityError(w, err)
			return
		}
		s.log.Info("user created", "user_id", user.ID)
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
	if len(parts) == 2 && parts[1] == "password" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		if _, _, ok := s.requireGlobalPermission(w, r, "Users.Manage", true); !ok {
			return
		}
		var in struct {
			Password string `json:"password"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := s.identity.ResetPassword(r.Context(), id, in.Password); err != nil {
			identityError(w, err)
			return
		}
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
			forbidden(w, "administrator access required to change administrator flag")
			return
		}
		user, err := s.identity.UpdateUser(r.Context(), actor.ID, id, in)
		if err != nil {
			identityError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"user": user})
	case http.MethodDelete:
		actor, _, ok := s.requireGlobalPermission(w, r, "Users.Manage", true)
		if !ok {
			return
		}
		if err := s.identity.DeleteUser(r.Context(), actor.ID, id); err != nil {
			identityError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}
func (s *Server) groupsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Groups.View", false); !ok {
			return
		}
		groups, err := s.identity.ListGroups(r.Context())
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"groups": groups})
	case http.MethodPost:
		if _, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true); !ok {
			return
		}
		var in identity.CreateGroupInput
		if !decode(w, r, &in) {
			return
		}
		group, err := s.identity.CreateGroup(r.Context(), in)
		if err != nil {
			identityError(w, err)
			return
		}
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
			if _, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true); !ok {
				return
			}
			var in struct {
				UserID string `json:"user_id"`
			}
			if !decode(w, r, &in) {
				return
			}
			if err := s.identity.AddMember(r.Context(), id, in.UserID); err != nil {
				identityError(w, err)
				return
			}
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
		if _, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true); !ok {
			return
		}
		if err := s.identity.RemoveMember(r.Context(), id, parts[2]); err != nil {
			identityError(w, err)
			return
		}
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
		if _, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true); !ok {
			return
		}
		var in identity.UpdateGroupInput
		if !decode(w, r, &in) {
			return
		}
		g, err := s.identity.UpdateGroup(r.Context(), id, in)
		if err != nil {
			identityError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"group": g})
	case http.MethodDelete:
		if _, _, ok := s.requireGlobalPermission(w, r, "Groups.Manage", true); !ok {
			return
		}
		if err := s.identity.DeleteGroup(r.Context(), id); err != nil {
			identityError(w, err)
			return
		}
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
		errorOut(w, http.StatusConflict, "last_active_admin", err.Error())
		return
	}
	if errors.Is(err, identity.ErrDuplicateMember) || strings.Contains(strings.ToLower(err.Error()), "constraint") {
		errorOut(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	message := err.Error()
	if strings.Contains(message, "must be") || strings.Contains(message, "valid email") {
		bad(w, message)
		return
	}
	internal(w)
}
