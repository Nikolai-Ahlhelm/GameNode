// Package ftp provides GameNode's embedded, per-server FTP/FTPS service.
// Authentication is independent from browser sessions: every local server has
// exactly one revocable credential and is rooted at that server's configured
// working directory.
package ftp

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"

	"gamenode/internal/auth"
	"gamenode/internal/filesystem"
)

var (
	ErrInvalidCredentials = errors.New("invalid FTP credentials")
	ErrCredentialMissing  = errors.New("FTP credentials have not been generated")
	ErrRootOverlap        = errors.New("FTP root overlaps another server root")
)

type Options struct {
	Enabled          bool
	ListenAddr       string
	PublicHost       string
	PassivePortStart int
	PassivePortEnd   int
	TLSCert          string
	TLSKey           string
	RequireTLS       bool
}

type Profile struct {
	ServerID       string    `json:"server_id"`
	Username       string    `json:"username"`
	Enabled        bool      `json:"enabled"`
	Configured     bool      `json:"configured"`
	ServiceEnabled bool      `json:"service_enabled"`
	ListenAddr     string    `json:"listen_address"`
	PublicHost     string    `json:"public_host,omitempty"`
	TLS            bool      `json:"tls"`
	TLSRequired    bool      `json:"tls_required"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Credential struct {
	Profile
	Password string `json:"password"`
}

type Service struct {
	db       *sql.DB
	files    *filesystem.Service
	options  Options
	log      *slog.Logger
	tls      *tls.Config
	server   *ftpserver.FtpServer
	fakeHash string
	mu       sync.Mutex
	started  bool
}

func New(db *sql.DB, files *filesystem.Service, options Options, log *slog.Logger) (*Service, error) {
	if db == nil || files == nil {
		return nil, errors.New("FTP database and filesystem service are required")
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Service{db: db, files: files, options: options, log: log}
	var err error
	s.fakeHash, err = auth.HashPassword("invalid-ftp-credential")
	if err != nil {
		return nil, fmt.Errorf("prepare FTP authentication: %w", err)
	}
	if options.TLSCert != "" {
		certificate, loadErr := tls.LoadX509KeyPair(options.TLSCert, options.TLSKey)
		if loadErr != nil {
			return nil, fmt.Errorf("load FTP TLS certificate: %w", loadErr)
		}
		s.tls = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	}
	return s, nil
}

func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.options.Enabled || s.started {
		return nil
	}
	server := ftpserver.NewFtpServer((*driver)(s))
	if err := server.Listen(); err != nil {
		return fmt.Errorf("listen for FTP connections: %w", err)
	}
	s.server = server
	s.started = true
	go func() {
		if err := server.Serve(); err != nil {
			s.mu.Lock()
			stopping := !s.started
			s.mu.Unlock()
			if !stopping {
				s.log.Error("FTP service stopped", "module", "FTP", "error", err)
			}
		}
	}()
	return nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	if !s.started || s.server == nil {
		s.mu.Unlock()
		return nil
	}
	s.started = false
	server := s.server
	s.mu.Unlock()
	return server.Stop()
}

func (s *Service) Profile(ctx context.Context, serverID string) (Profile, error) {
	var profile Profile
	var enabled int
	var configured int
	var updated string
	err := s.db.QueryRowContext(ctx, `
		SELECT f.server_id, f.username, f.enabled,
		       CASE WHEN f.password_hash <> '' THEN 1 ELSE 0 END, f.updated_at
		FROM server_ftp_credentials f
		JOIN servers s ON s.id=f.server_id
		WHERE f.server_id=?`, strings.TrimSpace(serverID)).
		Scan(&profile.ServerID, &profile.Username, &enabled, &configured, &updated)
	if err != nil {
		return Profile{}, err
	}
	profile.Enabled = enabled != 0
	profile.Configured = configured != 0
	profile.ServiceEnabled = s.options.Enabled
	profile.ListenAddr = s.options.ListenAddr
	profile.PublicHost = s.options.PublicHost
	profile.TLS = s.tls != nil
	profile.TLSRequired = s.options.RequireTLS
	profile.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return profile, nil
}

// Rotate replaces the credential, enables FTP access, and returns the new
// plaintext password exactly once. Only its Argon2id hash is persisted.
func (s *Service) Rotate(ctx context.Context, serverID string) (Credential, error) {
	if err := s.ensureExclusiveRoot(ctx, serverID); err != nil {
		return Credential{}, err
	}
	passwordBytes := make([]byte, 24)
	if _, err := rand.Read(passwordBytes); err != nil {
		return Credential{}, fmt.Errorf("generate FTP password: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(passwordBytes)
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Credential{}, fmt.Errorf("hash FTP password: %w", err)
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE server_ftp_credentials
		SET password_hash=?, enabled=1, updated_at=?
		WHERE server_id=?`, hash, now.Format(time.RFC3339Nano), strings.TrimSpace(serverID))
	if err != nil {
		return Credential{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Credential{}, err
	}
	if count == 0 {
		return Credential{}, sql.ErrNoRows
	}
	profile, err := s.Profile(ctx, serverID)
	if err != nil {
		return Credential{}, err
	}
	return Credential{Profile: profile, Password: password}, nil
}

func (s *Service) SetEnabled(ctx context.Context, serverID string, enabled bool) (Profile, error) {
	if enabled {
		if err := s.ensureExclusiveRoot(ctx, serverID); err != nil {
			return Profile{}, err
		}
		var configured int
		if err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN password_hash <> '' THEN 1 ELSE 0 END FROM server_ftp_credentials WHERE server_id=?`, strings.TrimSpace(serverID)).Scan(&configured); err != nil {
			return Profile{}, err
		}
		if configured == 0 {
			return Profile{}, ErrCredentialMissing
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE server_ftp_credentials SET enabled=?, updated_at=? WHERE server_id=?`, enabled, time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(serverID))
	if err != nil {
		return Profile{}, err
	}
	if count, countErr := result.RowsAffected(); countErr != nil {
		return Profile{}, countErr
	} else if count == 0 {
		return Profile{}, sql.ErrNoRows
	}
	return s.Profile(ctx, serverID)
}

// ensureExclusiveRoot prevents two FTP identities from becoming aliases for
// the same files. This matters for adopted servers, where administrators can
// intentionally register arbitrary existing directories.
func (s *Service) ensureExclusiveRoot(ctx context.Context, serverID string) error {
	var candidate string
	if err := s.db.QueryRowContext(ctx, "SELECT working_directory FROM servers WHERE id=?", strings.TrimSpace(serverID)).Scan(&candidate); err != nil {
		return err
	}
	candidate, err := canonicalRoot(candidate)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, working_directory FROM servers WHERE id<>?", strings.TrimSpace(serverID))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var other string
		var otherID string
		if err := rows.Scan(&otherID, &other); err != nil {
			return err
		}
		other, err = canonicalRoot(other)
		if err != nil {
			continue
		}
		if rootContains(candidate, other) || rootContains(other, candidate) {
			return ErrRootOverlap
		}
	}
	return rows.Err()
}

func canonicalRoot(root string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("FTP root is not a directory")
	}
	return filepath.Clean(abs), nil
}

func rootContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)))
}

func (s *Service) authenticate(ctx context.Context, username, password string) (string, error) {
	var hash, root string
	err := s.db.QueryRowContext(ctx, `
		SELECT f.password_hash, s.working_directory
		FROM server_ftp_credentials f
		JOIN servers s ON s.id=f.server_id
		WHERE f.username=? AND f.enabled=1`, strings.TrimSpace(username)).Scan(&hash, &root)
	if err != nil {
		// Perform the expensive comparison even for an unknown/disabled user so
		// the observable authentication cost does not disclose account state.
		_ = auth.VerifyPassword(s.fakeHash, password)
		return "", ErrInvalidCredentials
	}
	if !auth.VerifyPassword(hash, password) {
		return "", ErrInvalidCredentials
	}
	if _, err = s.files.ResolveServerPath(root, ""); err != nil {
		return "", ErrInvalidCredentials
	}
	return root, nil
}
