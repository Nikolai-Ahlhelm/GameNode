package api

import (
	"database/sql"
	"errors"
	"net/http"

	"gamenode/internal/audit"
	ftpservice "gamenode/internal/ftp"
)

func (s *Server) ftpHandler(w http.ResponseWriter, r *http.Request, serverID string, rest []string) {
	if s.ftp == nil {
		notFound(w)
		return
	}
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			if _, _, ok := s.requireServerPermission(w, r, "FTP.View", serverID, false); !ok {
				return
			}
			profile, err := s.ftp.Profile(r.Context(), serverID)
			if err != nil {
				ftpAPIError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			jsonOut(w, http.StatusOK, profile)
		case http.MethodPatch:
			actor, _, ok := s.requireServerPermission(w, r, "FTP.Manage", serverID, true)
			if !ok {
				return
			}
			serverName := ""
			if current, getErr := s.servers.Get(r.Context(), serverID); getErr == nil {
				serverName = current.Server.Name
			}
			var request struct {
				Enabled *bool `json:"enabled"`
			}
			if !decode(w, r, &request) {
				return
			}
			if request.Enabled == nil {
				bad(w, "enabled is required")
				return
			}
			profile, err := s.ftp.SetEnabled(r.Context(), serverID, *request.Enabled)
			action := audit.ServerFTPDisable
			if *request.Enabled {
				action = audit.ServerFTPEnable
			}
			if err != nil {
				s.recordServerAudit(r, actor, action, audit.Failure, serverID, serverName, err)
				ftpAPIError(w, err)
				return
			}
			s.recordServerAudit(r, actor, action, audit.Success, serverID, serverName, nil)
			w.Header().Set("Cache-Control", "no-store")
			jsonOut(w, http.StatusOK, profile)
		default:
			method(w)
		}
		return
	}

	if len(rest) == 1 && rest[0] == "credentials" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		actor, _, ok := s.requireServerPermission(w, r, "FTP.Manage", serverID, true)
		if !ok {
			return
		}
		serverName := ""
		if current, getErr := s.servers.Get(r.Context(), serverID); getErr == nil {
			serverName = current.Server.Name
		}
		credential, err := s.ftp.Rotate(r.Context(), serverID)
		if err != nil {
			s.recordServerAudit(r, actor, audit.ServerFTPCredentialRotate, audit.Failure, serverID, serverName, err)
			ftpAPIError(w, err)
			return
		}
		s.recordServerAudit(r, actor, audit.ServerFTPCredentialRotate, audit.Success, serverID, serverName, nil)
		w.Header().Set("Cache-Control", "no-store")
		jsonOut(w, http.StatusOK, credential)
		return
	}
	notFound(w)
}

func ftpAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		notFound(w)
	case errors.Is(err, ftpservice.ErrCredentialMissing):
		errorOut(w, http.StatusConflict, "ftp_credentials_missing", "Generate FTP credentials before enabling access")
	case errors.Is(err, ftpservice.ErrRootOverlap):
		errorOut(w, http.StatusConflict, "ftp_root_overlap", "This server's directory overlaps another server root")
	default:
		internal(w)
	}
}
