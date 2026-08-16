package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/tenants"
)

func (s *Server) recordTenantAudit(r *http.Request, actor auth.User, action, id, name string, metadata map[string]any, err error) {
	var resourceID *string
	if id != "" {
		resourceID = &id
	}
	result := audit.Success
	in := auditInput{action: action, resourceType: audit.Tenant, resourceID: resourceID, resourceName: name, result: result, actor: &actor}
	if err != nil {
		in.result = audit.Failure
		in.errorCode, in.errorSummary = tenantAuditFailure(err)
	} else if metadata != nil {
		in.metadata, _ = json.Marshal(metadata)
	}
	s.recordAudit(r, in)
}

func tenantAuditFailure(err error) (string, string) {
	switch {
	case errors.Is(err, tenants.ErrTenantNotFound):
		return "tenant_not_found", "tenant not found"
	case errors.Is(err, tenants.ErrDuplicateName):
		return "duplicate_name", "a tenant with this name already exists"
	case errors.Is(err, tenants.ErrDuplicateSlug):
		return "duplicate_slug", "a tenant with this slug already exists"
	case errors.Is(err, tenants.ErrTenantHasServers):
		return "tenant_has_servers", "tenant has servers and cannot be deleted"
	case errors.Is(err, tenants.ErrUserNotFound):
		return "user_not_found", "user not found"
	case errors.Is(err, tenants.ErrDuplicateMembership):
		return "duplicate_membership", "user is already a member of this tenant"
	case errors.Is(err, tenants.ErrMembershipNotFound):
		return "membership_not_found", "tenant membership not found"
	default:
		return "invalid_request", "invalid tenant request"
	}
}

// tenantError keeps raw SQLite/service errors out of the API, matching the
// existing rbacError/identityError/serverError convention.
func tenantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tenants.ErrTenantNotFound), errors.Is(err, tenants.ErrMembershipNotFound), errors.Is(err, sql.ErrNoRows):
		notFound(w)
	case errors.Is(err, tenants.ErrUserNotFound):
		errorOut(w, http.StatusBadRequest, "user_not_found", "user not found")
	case errors.Is(err, tenants.ErrDuplicateName), errors.Is(err, tenants.ErrDuplicateSlug), errors.Is(err, tenants.ErrDuplicateMembership):
		errorOut(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, tenants.ErrTenantHasServers):
		errorOut(w, http.StatusConflict, "tenant_has_servers", "Tenant has servers and cannot be deleted.")
	default:
		bad(w, err.Error())
	}
}

// tenantsHandler implements GET/POST /api/v1/tenants. Tenant entity reads and
// mutations are global-only Tenants.View/Tenants.Manage: they administer
// tenant entities themselves, not access to resources inside a tenant (see
// internal/rbac.GlobalOnly and docs/architecture.md).
func (s *Server) tenantsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Tenants.View", false); !ok {
			return
		}
		list, err := s.tenants.List(r.Context())
		if err != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"tenants": list})
	case http.MethodPost:
		actor, _, ok := s.requireGlobalPermission(w, r, "Tenants.Manage", true)
		if !ok {
			return
		}
		var in tenants.CreateInput
		if !decode(w, r, &in) {
			return
		}
		tenant, err := s.tenants.Create(r.Context(), in)
		if err != nil {
			s.recordTenantAudit(r, actor, audit.TenantCreate, "", in.Name, nil, err)
			tenantError(w, err)
			return
		}
		s.recordTenantAudit(r, actor, audit.TenantCreate, tenant.ID, tenant.Name, map[string]any{"slug": tenant.Slug}, nil)
		jsonOut(w, http.StatusCreated, map[string]any{"tenant": tenant})
	default:
		method(w)
	}
}

// tenantHandler implements /api/v1/tenants/{id} and its sub-resources:
// members, servers, and access. It follows the same manual path-splitting
// convention as serverHandler/userHandler.
func (s *Server) tenantHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/tenants/")
	parts := strings.Split(path, "/")
	if parts[0] == "" || len(parts) > 3 {
		notFound(w)
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "members" {
		s.tenantMembersHandler(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "members" {
		s.tenantMemberHandler(w, r, id, parts[2])
		return
	}
	if len(parts) == 2 && parts[1] == "servers" {
		s.tenantServersHandler(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "access" {
		s.tenantAccessHandler(w, r, id)
		return
	}
	if len(parts) != 1 {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Tenants.View", false); !ok {
			return
		}
		tenant, err := s.tenants.Get(r.Context(), id)
		if err != nil {
			tenantError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"tenant": tenant})
	case http.MethodPatch:
		actor, _, ok := s.requireGlobalPermission(w, r, "Tenants.Manage", true)
		if !ok {
			return
		}
		var in tenants.UpdateInput
		if !decode(w, r, &in) {
			return
		}
		tenant, err := s.tenants.Update(r.Context(), id, in)
		if err != nil {
			s.recordTenantAudit(r, actor, audit.TenantUpdate, id, "", nil, err)
			tenantError(w, err)
			return
		}
		s.recordTenantAudit(r, actor, audit.TenantUpdate, tenant.ID, tenant.Name, map[string]any{"slug": tenant.Slug}, nil)
		jsonOut(w, http.StatusOK, map[string]any{"tenant": tenant})
	case http.MethodDelete:
		actor, _, ok := s.requireGlobalPermission(w, r, "Tenants.Manage", true)
		if !ok {
			return
		}
		name := ""
		if existing, err := s.tenants.Get(r.Context(), id); err == nil {
			name = existing.Name
		}
		if err := s.tenants.Delete(r.Context(), id); err != nil {
			s.recordTenantAudit(r, actor, audit.TenantDelete, id, name, nil, err)
			tenantError(w, err)
			return
		}
		s.recordTenantAudit(r, actor, audit.TenantDelete, id, name, nil, nil)
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}

// tenantMembersHandler implements GET/POST /api/v1/tenants/{id}/members.
// Membership is a pure data fact (see internal/tenants); it never grants a
// permission by itself, so listing or changing it is gated by the same
// global Tenants.View/Tenants.Manage as the rest of tenant entity
// administration, not by any resource-access permission.
func (s *Server) tenantMembersHandler(w http.ResponseWriter, r *http.Request, tenantID string) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireGlobalPermission(w, r, "Tenants.View", false); !ok {
			return
		}
		members, err := s.tenants.ListMembers(r.Context(), tenantID)
		if err != nil {
			tenantError(w, err)
			return
		}
		jsonOut(w, http.StatusOK, map[string]any{"members": s.enrichMembers(r.Context(), members)})
	case http.MethodPost:
		actor, _, ok := s.requireGlobalPermission(w, r, "Tenants.Manage", true)
		if !ok {
			return
		}
		var in struct {
			UserID string `json:"user_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		membership, err := s.tenants.AddMember(r.Context(), tenantID, in.UserID)
		if err != nil {
			s.recordTenantAudit(r, actor, audit.TenantMemberAdd, tenantID, "", nil, err)
			tenantError(w, err)
			return
		}
		// Metadata is bounded to the two IDs involved; never the tenant's
		// full member list (see AGENTS.md audit rules).
		s.recordTenantAudit(r, actor, audit.TenantMemberAdd, tenantID, "", map[string]any{"user_id": membership.UserID}, nil)
		jsonOut(w, http.StatusCreated, map[string]any{"membership": membership})
	default:
		method(w)
	}
}

func (s *Server) tenantMemberHandler(w http.ResponseWriter, r *http.Request, tenantID, userID string) {
	if r.Method != http.MethodDelete {
		method(w)
		return
	}
	actor, _, ok := s.requireGlobalPermission(w, r, "Tenants.Manage", true)
	if !ok {
		return
	}
	if err := s.tenants.RemoveMember(r.Context(), tenantID, userID); err != nil {
		s.recordTenantAudit(r, actor, audit.TenantMemberRemove, tenantID, "", nil, err)
		tenantError(w, err)
		return
	}
	s.recordTenantAudit(r, actor, audit.TenantMemberRemove, tenantID, "", map[string]any{"user_id": userID}, nil)
	w.WriteHeader(http.StatusNoContent)
}

// enrichMembers adds each member's username for display, tolerating a user
// that no longer exists (falls back to no username) rather than failing the
// whole listing.
func (s *Server) enrichMembers(ctx context.Context, members []tenants.Membership) []map[string]any {
	result := make([]map[string]any, 0, len(members))
	for _, member := range members {
		username := ""
		if user, err := s.identity.GetUser(ctx, member.UserID); err == nil {
			username = user.Username
		}
		result = append(result, map[string]any{"tenant_id": member.TenantID, "user_id": member.UserID, "username": username, "created_at": member.CreatedAt})
	}
	return result
}

// tenantServersHandler implements GET /api/v1/tenants/{id}/servers: every
// server owned by this tenant. Gated by global Tenants.View, distinct from
// the per-server Server.View used by GET /servers - this is an
// administrative "what does this tenant own" listing, not the ordinary
// RBAC-filtered server list.
func (s *Server) tenantServersHandler(w http.ResponseWriter, r *http.Request, tenantID string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Tenants.View", false); !ok {
		return
	}
	if _, err := s.tenants.Get(r.Context(), tenantID); err != nil {
		tenantError(w, err)
		return
	}
	records, err := s.servers.List(r.Context())
	if err != nil {
		internal(w)
		return
	}
	result := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if record.Server.TenantID != tenantID {
			continue
		}
		record, err = s.publicServerRecord(r.Context(), record)
		if err != nil {
			internal(w)
			return
		}
		result = append(result, map[string]any{"server": record.Server, "runtime": record.Runtime})
	}
	jsonOut(w, http.StatusOK, map[string]any{"servers": result})
}

// tenantAccessHandler implements GET /api/v1/tenants/{id}/access: RBAC role
// assignments scoped to this tenant. It reuses rbac.Service's existing
// assignment tables exactly like GET /servers/{id}/access does for server
// scope, and is gated by the same global Roles.View that already governs
// reading assignment data - not Tenants.View, which governs the tenant
// entity itself.
func (s *Server) tenantAccessHandler(w http.ResponseWriter, r *http.Request, tenantID string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if _, _, ok := s.requireGlobalPermission(w, r, "Roles.View", false); !ok {
		return
	}
	assignments, err := s.rbac.ListTenantAssignments(r.Context(), tenantID)
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"assignments": assignments})
}
