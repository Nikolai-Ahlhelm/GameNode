package api

import (
	"errors"
	"gamenode/internal/passwordreset"
	"net/http"
	"strings"
)

type passwordResetRequest struct {
	Email string `json:"email"`
}
type passwordResetConsume struct {
	ResetID  string `json:"reset_id"`
	Token    string `json:"token"`
	Password string `json:"password"`
}

func (s *Server) passwordResetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/password-reset")
	if path == "" || path == "/" {
		var in passwordResetRequest
		if !decode(w, r, &in) {
			return
		}
		result, e := s.passwordReset.Request(r.Context(), in.Email, s.publicOrigin(r))
		if e != nil {
			internal(w)
			return
		}
		jsonOut(w, http.StatusAccepted, result)
		return
	}
	if path != "/consume" {
		http.NotFound(w, r)
		return
	}
	var in passwordResetConsume
	if !decode(w, r, &in) {
		return
	}
	e := s.passwordReset.Consume(r.Context(), in.ResetID, in.Token, in.Password)
	if e != nil {
		status := http.StatusBadRequest
		if errors.Is(e, passwordreset.ErrNotFound) || errors.Is(e, passwordreset.ErrInvalidToken) {
			status = http.StatusNotFound
		}
		http.Error(w, "password reset unavailable", status)
		return
	}
	jsonOut(w, http.StatusOK, map[string]bool{"reset": true})
}
