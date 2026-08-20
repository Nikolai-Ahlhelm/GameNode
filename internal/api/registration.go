package api

import (
	"errors"
	"gamenode/internal/rbac"
	"gamenode/internal/registration"
	"net/http"
	"strings"
)

type inviteRequest struct {
	Email string `json:"email"`
}
type registerRequest struct {
	InvitationID string `json:"invitation_id"`
	Token        string `json:"token"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Password     string `json:"password"`
}

func (s *Server) registrationHandler(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/registration")
	if path != "" && path != "/" {
		if r.Method == http.MethodGet {
			id := strings.TrimPrefix(path, "/")
			p, e := s.registration.Preview(r.Context(), id, r.URL.Query().Get("token"))
			if e != nil {
				http.Error(w, "invitation unavailable", http.StatusNotFound)
				return
			}
			jsonOut(w, http.StatusOK, p)
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in registerRequest
	if !decode(w, r, &in) {
		return
	}
	u, e := s.registration.Register(r.Context(), registration.RegisterInput{InvitationID: in.InvitationID, Token: in.Token, Username: in.Username, DisplayName: in.DisplayName, Password: in.Password})
	if e != nil {
		status := http.StatusBadRequest
		if errors.Is(e, registration.ErrInvitationNotFound) || errors.Is(e, registration.ErrInvalidToken) {
			status = http.StatusNotFound
		}
		http.Error(w, "registration unavailable", status)
		return
	}
	jsonOut(w, http.StatusCreated, u)
}

func (s *Server) tenantInvitationHandler(w http.ResponseWriter, r *http.Request, tenantID string) {
	actor, _, ok := s.requireAuth(w, r, true)
	if !ok {
		return
	}
	permissionGranted, err := s.allowed(r.Context(), actor, "Tenants.Invite", rbac.Scope{Type: "tenant", ID: &tenantID})
	if err != nil {
		internal(w)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in inviteRequest
	if !decode(w, r, &in) {
		return
	}
	inv, e := s.registration.Invite(r.Context(), tenantID, actor.ID, in.Email, s.publicOrigin(r), permissionGranted)
	if e != nil {
		http.Error(w, "invitation could not be created", http.StatusBadRequest)
		return
	}
	jsonOut(w, http.StatusCreated, inv)
}

func (s *Server) publicOrigin(r *http.Request) string {
	scheme := "http"
	host := r.Host
	if r.TLS != nil {
		scheme = "https"
	} else if s.trustLocalProxy && isLoopbackPeer(r.RemoteAddr) {
		if v := singleForwardedValue(r.Header.Get("X-Forwarded-Proto")); v == "http" || v == "https" {
			scheme = v
		}
		if v := singleForwardedValue(r.Header.Get("X-Forwarded-Host")); v != "" {
			host = v
		}
	}
	return scheme + "://" + host
}
