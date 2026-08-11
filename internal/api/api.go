package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/dashboard"
	"gamenode/internal/diagnostics"
	"gamenode/internal/filesystem"
	"gamenode/internal/identity"
	"gamenode/internal/monitoring"
	"gamenode/internal/ports"
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	"gamenode/internal/servers"
	"gamenode/internal/settings"
	"gamenode/internal/support"
	"gamenode/internal/templates"
)

const sessionCookie = "gamenode_session"

type Server struct {
	auth         *auth.Service
	audit        *audit.Service
	log          *slog.Logger
	secureCookie bool
	servers      *servers.Service
	files        *filesystem.Service
	identity     *identity.Service
	rbac         *rbac.Service
	ports        *ports.Service
	settings     *settings.Service
	diagnostics  *diagnostics.Service
	support      supportGenerator
	templates    *templates.Service
	provisioning *provisioning.Service
}

type supportGenerator interface {
	Generate(context.Context, io.Writer, support.Scope) error
}

type Options struct {
	Filesystem   *filesystem.Service
	Settings     *settings.Service
	Diagnostics  *diagnostics.Service
	Support      supportGenerator
	Templates    *templates.Service
	Provisioning *provisioning.Service
}

// auditInput deliberately contains only values selected by the application. It
// must never be populated from a request body or credential material.
type auditInput struct {
	action       string
	resourceType string
	resourceID   *string
	resourceName string
	serverID     *string
	result       string
	metadata     json.RawMessage
	errorCode    string
	errorSummary string
	actor        *auth.User
}

func (s *Server) recordAudit(r *http.Request, in auditInput) {
	event := audit.Event{
		Action:       in.action,
		ResourceType: in.resourceType,
		ResourceID:   in.resourceID,
		ResourceName: in.resourceName,
		ServerID:     in.serverID,
		Result:       in.result,
		Metadata:     in.metadata,
		ErrorCode:    in.errorCode,
		ErrorSummary: in.errorSummary,
	}
	if in.actor != nil {
		actorID := in.actor.ID
		event.ActorUserID = &actorID
		event.ActorUsername = in.actor.Username
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		event.RemoteIP = host
	} else {
		event.RemoteIP = r.RemoteAddr
	}
	if err := s.audit.Record(r.Context(), event); err != nil {
		s.log.Error("audit write failed", "error", err.Error(), "action", in.action)
	}
}

// auditFailure intentionally exposes only stable, non-sensitive summaries.
// Runtime and persistence errors can include host-specific implementation details.
func auditFailure(err error) (string, string) {
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "port preflight:"):
		return "port_preflight_failed", "port preflight failed"
	case strings.Contains(message, "port conflicts") || strings.Contains(message, "port is already in use"):
		return "port_conflict", "port assignment conflicts with an existing listener"
	case strings.Contains(message, "port must be"):
		return "invalid_port", "invalid port assignment"
	case strings.Contains(message, "protocol must be"):
		return "invalid_protocol", "invalid port assignment"
	case strings.Contains(message, "bind address must be"):
		return "invalid_bind_address", "invalid port assignment"
	case strings.Contains(message, "already running") || strings.Contains(message, "not running") || strings.Contains(message, "restart is in progress") || strings.Contains(message, "stop the server before"):
		return "invalid_state", "server state does not allow this operation"
	case errors.Is(err, filesystem.ErrInvalidPath), errors.Is(err, filesystem.ErrPathEscapesRoot), errors.Is(err, filesystem.ErrInvalidFilename), errors.Is(err, filesystem.ErrRootOperation), errors.Is(err, filesystem.ErrExpectedFile), errors.Is(err, filesystem.ErrExpectedDir), errors.Is(err, filesystem.ErrSpecialFile):
		return "invalid_path", "filesystem path is not available"
	case errors.Is(err, filesystem.ErrAlreadyExists), errors.Is(err, filesystem.ErrDirectoryNotEmpty):
		return "file_conflict", "filesystem operation conflicts with existing content"
	case errors.Is(err, filesystem.ErrNotFound):
		return "not_found", "filesystem path not found"
	case errors.Is(err, filesystem.ErrTooLarge):
		return "file_too_large", "filesystem operation exceeds the size limit"
	default:
		return "operation_failed", "operation failed"
	}
}

func New(a *auth.Service, serverService *servers.Service, log *slog.Logger, secureCookie bool, options ...Options) *Server {
	files := filesystem.New()
	if len(options) > 0 && options[0].Filesystem != nil {
		files = options[0].Filesystem
	}
	settingService := settings.New(a.Database(), settings.Defaults{})
	if len(options) > 0 && options[0].Settings != nil {
		settingService = options[0].Settings
	}
	diagnosticService := diagnostics.New(a.Database(), settingService, diagnostics.MonitoringEffective{}, time.Now().UTC())
	if len(options) > 0 && options[0].Diagnostics != nil {
		diagnosticService = options[0].Diagnostics
	}
	var supportService supportGenerator = support.New(diagnosticService, settingService, audit.New(a.Database()), serverService)
	if len(options) > 0 && options[0].Support != nil {
		supportService = options[0].Support
	}
	templateService := templates.NewService(templates.NewStore(a.Database()))
	var provisioner *provisioning.Service
	if len(options) > 0 {
		if options[0].Templates != nil {
			templateService = options[0].Templates
		}
		provisioner = options[0].Provisioning
	}
	result := &Server{auth: a, audit: audit.New(a.Database()), servers: serverService, files: files, identity: identity.New(a.Database()), rbac: rbac.New(a.Database()), ports: ports.New(a.Database()), settings: settingService, diagnostics: diagnosticService, support: supportService, templates: templateService, provisioning: provisioner, log: log, secureCookie: secureCookie}
	if provisioner != nil {
		provisioner.SetObserver(result.recordProvisioningCompletion)
	}
	return result
}
func (s *Server) Handler(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/setup/status", s.setupStatus)
	mux.HandleFunc("/api/v1/setup", s.setup)
	mux.HandleFunc("/api/v1/auth/login", s.login)
	mux.HandleFunc("/api/v1/auth/logout", s.logout)
	mux.HandleFunc("/api/v1/auth/me", s.me)
	mux.HandleFunc("/api/v1/dashboard", s.dashboard)
	mux.HandleFunc("/api/v1/audit", s.auditHandler)
	mux.HandleFunc("/api/v1/settings", s.settingsHandler)
	mux.HandleFunc("/api/v1/diagnostics", s.diagnosticsHandler)
	mux.HandleFunc("/api/v1/support/bundle", s.supportBundleHandler)
	mux.HandleFunc("/api/v1/users", s.usersHandler)
	mux.HandleFunc("/api/v1/users/", s.userHandler)
	mux.HandleFunc("/api/v1/groups", s.groupsHandler)
	mux.HandleFunc("/api/v1/groups/", s.groupHandler)
	mux.HandleFunc("/api/v1/permissions", s.permissionsHandler)
	mux.HandleFunc("/api/v1/roles", s.rolesHandler)
	mux.HandleFunc("/api/v1/roles/", s.roleHandler)
	mux.HandleFunc("/api/v1/servers", s.serversHandler)
	mux.HandleFunc("/api/v1/servers/", s.serverHandler)
	mux.HandleFunc("/api/v1/templates", s.templatesHandler)
	mux.HandleFunc("/api/v1/templates/", s.templateHandler)
	mux.HandleFunc("/api/v1/provisioning/jobs/", s.provisioningJobHandler)
	mux.Handle("/", static)
	return s.logRequests(mux)
}

type credentials struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		method(w)
		return
	}
	required, e := s.auth.SetupRequired(r.Context())
	if e != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]bool{"setup_required": required})
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		method(w)
		return
	}
	if !sameOrigin(r) {
		forbidden(w, "cross-origin request rejected")
		return
	}
	var in credentials
	if !decode(w, r, &in) {
		return
	}
	u, e := s.auth.CreateInitialAdmin(r.Context(), in.Username, in.Email, in.Password)
	if e != nil {
		s.log.Warn("initial administrator creation failed", "reason", e.Error())
		bad(w, "initial setup could not be completed")
		return
	}
	s.log.Info("initial administrator created", "user_id", u.ID)
	s.issueLogin(w, r, u, in.Password)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		method(w)
		return
	}
	if !sameOrigin(r) {
		forbidden(w, "cross-origin request rejected")
		return
	}
	var in credentials
	if !decode(w, r, &in) {
		return
	}
	s.issueLogin(w, r, auth.User{}, in.Password, in.Username)
}
func (s *Server) issueLogin(w http.ResponseWriter, r *http.Request, u auth.User, password string, username ...string) {
	if len(username) > 0 {
		var e error
		var raw, csrf string
		u, raw, csrf, e = s.auth.Login(r.Context(), username[0], password)
		if e != nil {
			s.recordAudit(r, auditInput{action: audit.Login, resourceType: audit.Auth, resourceName: strings.TrimSpace(username[0]), result: audit.Failure, errorCode: "invalid_credentials", errorSummary: "invalid credentials"})
			s.log.Warn("failed login", "source_ip", r.RemoteAddr)
			unauthorized(w)
			return
		}
		s.setSessionAndRespond(r.Context(), w, u, raw, csrf)
		s.recordAudit(r, auditInput{action: audit.Login, resourceType: audit.Auth, result: audit.Success, actor: &u})
		return
	}
	raw, csrf, e := s.auth.CreateSession(r.Context(), u)
	if e != nil {
		internal(w)
		return
	}
	s.setSessionAndRespond(r.Context(), w, u, raw, csrf)
}
func (s *Server) setSessionAndRespond(ctx context.Context, w http.ResponseWriter, u auth.User, raw, csrf string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: raw, Path: "/", HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: int((24 * time.Hour).Seconds())})
	s.log.Info("user logged in", "user_id", u.ID)
	capabilities, err := s.globalCapabilities(ctx, u)
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"user": u, "csrf_token": csrf, "capabilities": capabilities})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		method(w)
		return
	}
	u, csrf, ok := s.requireAuth(w, r, false)
	if !ok {
		return
	}
	capabilities, err := s.globalCapabilities(r.Context(), u)
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"user": u, "csrf_token": csrf, "capabilities": capabilities})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		method(w)
		return
	}
	u, _, ok := s.requireAuth(w, r, true)
	if !ok {
		return
	}
	c, _ := r.Cookie(sessionCookie)
	if err := s.auth.Logout(r.Context(), c.Value); err != nil {
		internal(w)
		return
	}
	s.recordAudit(r, auditInput{action: audit.Logout, resourceType: audit.Auth, result: audit.Success, actor: &u})
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	s.log.Info("user logged out", "user_id", u.ID)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		method(w)
		return
	}
	u, _, ok := s.requireAuth(w, r, false)
	if !ok {
		return
	}
	records, err := s.servers.List(r.Context())
	if err != nil {
		internal(w)
		return
	}
	visible := []dashboard.Server{}
	visiblePorts := []dashboard.Port{}
	for _, record := range records {
		allowed, err := s.allowed(r.Context(), u, "Server.View", rbac.Scope{Type: "server", ID: &record.Server.ID})
		if err != nil {
			internal(w)
			return
		}
		if !allowed {
			continue
		}
		monitorAllowed, err := s.allowed(r.Context(), u, "Monitoring.View", rbac.Scope{Type: "server", ID: &record.Server.ID})
		if err != nil {
			internal(w)
			return
		}
		snap := monitoring.Snapshot{}
		if monitorAllowed {
			snap, _ = s.servers.MonitoringSnapshot(r.Context(), record.Server.ID)
		}
		visible = append(visible, dashboard.Server{State: record.Runtime.CurrentState, Monitoring: snap})
		portsAllowed, err := s.allowed(r.Context(), u, "Ports.View", rbac.Scope{Type: "server", ID: &record.Server.ID})
		if err != nil {
			internal(w)
			return
		}
		if portsAllowed {
			ps, _ := s.ports.List(r.Context(), record.Server.ID)
			for _, p := range ps {
				visiblePorts = append(visiblePorts, dashboard.Port{Protocol: p.Protocol})
			}
		}
	}
	summary := dashboard.Aggregate(visible, visiblePorts)
	auditAvailable, err := s.allowed(r.Context(), u, "Audit.View", rbac.Scope{Type: "global"})
	if err != nil {
		internal(w)
		return
	}
	response := map[string]any{"user": u, "servers": summary.Servers, "monitoring": summary.Monitoring, "ports": summary.Ports, "audit": map[string]any{"available": auditAvailable, "recent": []audit.Event{}}}
	if auditAvailable {
		events, e := s.audit.List(r.Context(), audit.Filter{Limit: 10})
		if e == nil {
			response["audit"] = map[string]any{"available": true, "recent": events}
		}
	}
	jsonOut(w, http.StatusOK, response)
}

var productPermissions = []string{"Server.View", "Server.Create", "Server.Edit", "Server.Delete", "Server.Start", "Server.Stop", "Server.Restart", "Server.Kill", "Console.View", "Console.Send", "Files.View", "Files.Edit", "Files.Upload", "Files.Download", "Files.Delete", "Files.Rename", "Ports.View", "Ports.Manage", "Users.View", "Users.Manage", "Groups.View", "Groups.Manage", "Roles.View", "Roles.Manage", "Settings.View", "Settings.Manage", "Templates.View", "Templates.Manage", "Monitoring.View", "Audit.View"}

func (s *Server) allowed(ctx context.Context, u auth.User, permission string, scope rbac.Scope) (bool, error) {
	return s.rbac.Allowed(ctx, u.ID, permission, scope)
}

func (s *Server) requirePermission(w http.ResponseWriter, r *http.Request, permission string, scope rbac.Scope, csrfRequired bool) (auth.User, string, bool) {
	u, csrf, ok := s.requireAuth(w, r, csrfRequired)
	if !ok {
		return auth.User{}, "", false
	}
	allowed, err := s.allowed(r.Context(), u, permission, scope)
	if err != nil {
		internal(w)
		return auth.User{}, "", false
	}
	if !allowed {
		forbidden(w, "permission denied")
		return auth.User{}, "", false
	}
	return u, csrf, true
}

func (s *Server) requireServerPermission(w http.ResponseWriter, r *http.Request, permission, serverID string, csrfRequired bool) (auth.User, string, bool) {
	return s.requirePermission(w, r, permission, rbac.Scope{Type: "server", ID: &serverID}, csrfRequired)
}

func (s *Server) requireGlobalPermission(w http.ResponseWriter, r *http.Request, permission string, csrfRequired bool) (auth.User, string, bool) {
	return s.requirePermission(w, r, permission, rbac.Scope{Type: "global"}, csrfRequired)
}

func (s *Server) globalCapabilities(ctx context.Context, u auth.User) ([]string, error) {
	return s.capabilities(ctx, u, rbac.Scope{Type: "global"})
}

func (s *Server) serverCapabilities(ctx context.Context, u auth.User, serverID string) ([]string, error) {
	return s.capabilities(ctx, u, rbac.Scope{Type: "server", ID: &serverID})
}

func (s *Server) capabilities(ctx context.Context, u auth.User, scope rbac.Scope) ([]string, error) {
	result := make([]string, 0, len(productPermissions))
	for _, permission := range productPermissions {
		allowed, err := s.allowed(ctx, u, permission, scope)
		if err != nil {
			return nil, err
		}
		if allowed {
			result = append(result, permission)
		}
	}
	return result, nil
}

func (s *Server) visibleServers(ctx context.Context, u auth.User) ([]map[string]any, error) {
	records, err := s.servers.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(records))
	for _, record := range records {
		capabilities, err := s.serverCapabilities(ctx, u, record.Server.ID)
		if err != nil {
			return nil, err
		}
		if !containsCapability(capabilities, "Server.View") {
			continue
		}
		record, err = s.publicServerRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"server": record.Server, "runtime": record.Runtime, "capabilities": capabilities})
	}
	return result, nil
}

func (s *Server) publicServerRecord(ctx context.Context, record servers.Record) (servers.Record, error) {
	keys, err := s.servers.SensitiveEnvironmentKeys(ctx, record.Server.ID)
	if err != nil {
		return servers.Record{}, err
	}
	if len(keys) == 0 {
		return record, nil
	}
	environment := make(map[string]string, len(record.Server.EnvironmentVariables))
	arguments := append([]string(nil), record.Server.Arguments...)
	for key, value := range record.Server.EnvironmentVariables {
		environment[key] = value
	}
	for _, key := range keys {
		secret := record.Server.EnvironmentVariables[key]
		delete(environment, key)
		if secret != "" {
			for index, argument := range arguments {
				arguments[index] = strings.ReplaceAll(argument, secret, "********")
			}
		}
	}
	record.Server.Arguments = arguments
	record.Server.EnvironmentVariables = environment
	record.Server.SensitiveEnvironmentVariables = keys
	return record, nil
}

func containsCapability(capabilities []string, permission string) bool {
	for _, capability := range capabilities {
		if capability == permission {
			return true
		}
	}
	return false
}
func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request, csrfRequired bool) (auth.User, string, bool) {
	c, e := r.Cookie(sessionCookie)
	if e != nil {
		unauthorized(w)
		return auth.User{}, "", false
	}
	u, csrf, e := s.auth.Current(r.Context(), c.Value)
	if e != nil {
		unauthorized(w)
		return auth.User{}, "", false
	}
	if csrfRequired && (!sameOrigin(r) || r.Header.Get("X-CSRF-Token") != csrf) {
		forbidden(w, "csrf validation failed")
		return auth.User{}, "", false
	}
	return u, csrf, true
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return e == nil && u.Host == r.Host && u.Scheme == scheme
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	de := json.NewDecoder(r.Body)
	de.DisallowUnknownFields()
	if de.Decode(v) != nil {
		bad(w, "invalid request body")
		return false
	}
	// A request body represents exactly one JSON document. Without this check,
	// a valid prefix followed by a second payload would be silently accepted.
	if de.Decode(&struct{}{}) != io.EOF {
		bad(w, "invalid request body")
		return false
	}
	return true
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func errorOut(w http.ResponseWriter, status int, code, message string) {
	jsonOut(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func method(w http.ResponseWriter)        { errorOut(w, 405, "method_not_allowed", "method not allowed") }
func bad(w http.ResponseWriter, m string) { errorOut(w, 400, "invalid_request", m) }
func unauthorized(w http.ResponseWriter) {
	errorOut(w, 401, "unauthenticated", "invalid username or password")
}
func forbidden(w http.ResponseWriter, m string) { errorOut(w, 403, "forbidden", m) }
func internal(w http.ResponseWriter) {
	errorOut(w, 500, "internal_error", "an internal error occurred")
}
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("http request", "method", r.Method, "path", strings.Split(r.URL.Path, "?")[0], "duration", time.Since(start).String())
	})
}
