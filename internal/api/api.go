package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/dashboard"
	"gamenode/internal/diagnostics"
	"gamenode/internal/filesystem"
	"gamenode/internal/gameconfig"
	"gamenode/internal/identity"
	"gamenode/internal/logging"
	"gamenode/internal/monitoring"
	"gamenode/internal/nodeidentity"
	"gamenode/internal/nodes"
	"gamenode/internal/ports"
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
	"gamenode/internal/scheduler"
	"gamenode/internal/servers"
	"gamenode/internal/serverupdates"
	"gamenode/internal/settings"
	"gamenode/internal/steamcmd"
	"gamenode/internal/support"
	"gamenode/internal/templates"
	"gamenode/internal/tenants"
)

const sessionCookie = "gamenode_session"

type Server struct {
	auth             *auth.Service
	audit            *audit.Service
	log              *slog.Logger
	secureCookie     bool
	trustLocalProxy  bool
	servers          *servers.Service
	files            *filesystem.Service
	identity         *identity.Service
	rbac             *rbac.Service
	tenants          *tenants.Service
	ports            *ports.Service
	settings         *settings.Service
	diagnostics      *diagnostics.Service
	support          supportGenerator
	templates        *templates.Service
	provisioning     *provisioning.Service
	serverUpdates    *serverupdates.Service
	restartSchedules *scheduler.Store
	restartScheduler *scheduler.Scheduler
	gameConfig       *gameconfig.Service
	logs             *logging.Manager
	setupConfig      setupConfigStore
	steamcmd         steamBootstrapper
	bootstrapMu      sync.Mutex
	bootstrap        bootstrapStatus
	nodeIdentity     *nodeidentity.Service
	nodes            *nodes.Service
	remoteClient     remoteNodeClient
}

// remoteNodeClient is the narrow set of typed Remote Node operations the API
// layer needs. internal/remote.Client satisfies it; tests may substitute a
// fake. There is no generic "do arbitrary request" method here (see
// AGENTS.md and internal/remote's package doc comment).
type remoteNodeClient interface {
	Enroll(ctx context.Context, endpoint, pairingToken string) (remote.EnrollResult, error)
	GetNodeInfo(ctx context.Context, endpoint, credential string) (remote.NodeInfo, error)
	GetHealth(ctx context.Context, endpoint, credential string) (remote.HealthResult, error)
}

type setupConfigStore interface {
	Storage() (string, string)
	SetStorage(string, string) error
}
type steamBootstrapper interface {
	Detect() bool
	Ensure(context.Context, steamcmd.EventSink) error
}
type bootstrapStatus struct {
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type supportGenerator interface {
	Generate(context.Context, io.Writer, support.Scope) error
}

type Options struct {
	// TrustLocalProxy permits forwarded scheme and host headers only when the
	// immediate peer is a loopback reverse proxy. It must not be used for a
	// proxy reached over the network.
	TrustLocalProxy  bool
	Filesystem       *filesystem.Service
	Settings         *settings.Service
	Diagnostics      *diagnostics.Service
	Support          supportGenerator
	Templates        *templates.Service
	Provisioning     *provisioning.Service
	ServerUpdates    *serverupdates.Service
	RestartSchedules *scheduler.Store
	RestartScheduler *scheduler.Scheduler
	GameConfig       *gameconfig.Service
	Logs             *logging.Manager
	SetupConfig      setupConfigStore
	SteamCMD         steamBootstrapper
	NodeIdentity     *nodeidentity.Service
	RemoteNodes      *nodes.Service
	RemoteClient     remoteNodeClient
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
	// err is the original, unsanitized error behind errorCode/errorSummary,
	// if any. It is never persisted to the audit log or included in any API
	// response - recordAudit only ever attaches it to the local application
	// log, and only when detailed error logging is enabled (see
	// logging.ErrorDetail). This is the sole place callers need to pass the
	// raw error; audit.Event itself only ever receives the sanitized summary.
	err error
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
		s.log.With("module", "Audit.Record").Error("audit write failed", "error", err.Error(), "action", in.action)
	}
	attrs := []any{"module", "Action", "action", in.action, "resource_type", in.resourceType, "result", in.result}
	if in.actor != nil {
		attrs = append(attrs, "actor_user_id", in.actor.ID)
	}
	if in.serverID != nil {
		attrs = append(attrs, "server_id", *in.serverID)
	}
	if in.resourceID != nil {
		attrs = append(attrs, "resource_id", *in.resourceID)
	}
	if in.errorCode != "" {
		attrs = append(attrs, "error_code", in.errorCode, "error_summary", in.errorSummary)
	}
	if in.err != nil {
		attrs = append(attrs, logging.ErrorDetail(in.err))
	}
	if in.result == audit.Success {
		s.log.Info("application action completed", attrs...)
	} else {
		s.log.Warn("application action failed", attrs...)
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
	case errors.Is(err, servers.ErrDuplicateName), errors.Is(err, provisioning.ErrNamePreflightFailed):
		return "name_conflict", "server name is already in use"
	case strings.Contains(message, "already running") || strings.Contains(message, "not running") || strings.Contains(message, "restart is in progress") || strings.Contains(message, "stop the server before"):
		return "invalid_state", "server state does not allow this operation"
	case strings.Contains(message, "container engine is unavailable"):
		return "container_engine_unavailable", "container engine is unavailable"
	case strings.Contains(message, "container ownership is invalid"):
		return "container_ownership_invalid", "container ownership could not be verified"
	case strings.Contains(message, "container image is missing"):
		return "container_image_missing", "configured container image is not available"
	case strings.Contains(message, "container image pull is already in progress"):
		return "container_pull_in_progress", "container image pull is already in progress"
	case strings.Contains(message, "container image"):
		return "container_runtime_invalid", "container configuration is invalid"
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
	a.SetPasswordPolicyProvider(settingService)
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
	var serverUpdater *serverupdates.Service
	if len(options) > 0 {
		serverUpdater = options[0].ServerUpdates
	}
	var gameConfigService *gameconfig.Service
	if len(options) > 0 {
		gameConfigService = options[0].GameConfig
	}
	var logManager *logging.Manager
	if len(options) > 0 {
		logManager = options[0].Logs
	}
	identityService := identity.New(a.Database())
	identityService.SetPasswordPolicyProvider(settingService)
	nodeIdentityService := nodeidentity.New(a.Database(), diagnostics.Version)
	nodesService := nodes.New(a.Database())
	var remoteClient remoteNodeClient = remote.New()
	result := &Server{auth: a, audit: audit.New(a.Database()), servers: serverService, files: files, identity: identityService, rbac: rbac.New(a.Database()), tenants: tenants.New(a.Database()), ports: ports.New(a.Database()), settings: settingService, diagnostics: diagnosticService, support: supportService, templates: templateService, provisioning: provisioner, serverUpdates: serverUpdater, gameConfig: gameConfigService, logs: logManager, log: log, secureCookie: secureCookie, nodeIdentity: nodeIdentityService, nodes: nodesService, remoteClient: remoteClient}
	if len(options) > 0 {
		result.trustLocalProxy = options[0].TrustLocalProxy
		result.setupConfig = options[0].SetupConfig
		result.steamcmd = options[0].SteamCMD
		result.restartSchedules = options[0].RestartSchedules
		result.restartScheduler = options[0].RestartScheduler
		if options[0].NodeIdentity != nil {
			result.nodeIdentity = options[0].NodeIdentity
		}
		if options[0].RemoteNodes != nil {
			result.nodes = options[0].RemoteNodes
		}
		if options[0].RemoteClient != nil {
			result.remoteClient = options[0].RemoteClient
		}
	}
	result.bootstrap = bootstrapStatus{Status: "unavailable", Summary: "SteamCMD setup is unavailable"}
	if result.steamcmd != nil {
		result.bootstrap = bootstrapStatus{Status: "idle", Summary: "SteamCMD has not been prepared"}
		if result.steamcmd.Detect() {
			result.bootstrap = bootstrapStatus{Status: "ready", Summary: "SteamCMD is ready"}
		}
	}
	if provisioner != nil {
		provisioner.SetObserver(result.recordProvisioningCompletion)
	}
	if serverUpdater != nil {
		serverUpdater.SetObserver(result.recordServerUpdateCompletion)
	}
	return result
}
func (s *Server) Handler(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/setup/status", s.setupStatus)
	mux.HandleFunc("/api/v1/setup/config", s.setupConfigHandler)
	mux.HandleFunc("/api/v1/setup/steamcmd", s.setupSteamCMDStatus)
	mux.HandleFunc("/api/v1/setup", s.setup)
	mux.HandleFunc("/api/v1/auth/login", s.login)
	mux.HandleFunc("/api/v1/auth/logout", s.logout)
	mux.HandleFunc("/api/v1/auth/me", s.me)
	mux.HandleFunc("/api/v1/dashboard", s.dashboard)
	mux.HandleFunc("/api/v1/audit", s.auditHandler)
	mux.HandleFunc("/api/v1/settings", s.settingsHandler)
	mux.HandleFunc("/api/v1/settings/favicon", s.settingsFaviconHandler)
	mux.HandleFunc("/api/v1/branding/favicon", s.brandingFaviconHandler)
	mux.HandleFunc("/api/v1/settings/logs", s.applicationLogsHandler)
	mux.HandleFunc("/api/v1/settings/logs/clear", s.clearLogsHandler)
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
	mux.HandleFunc("/api/v1/servers/creatable-tenants", s.creatableTenantsHandler)
	mux.HandleFunc("/api/v1/servers/", s.serverHandler)
	mux.HandleFunc("/api/v1/tenants", s.tenantsHandler)
	mux.HandleFunc("/api/v1/tenants/", s.tenantHandler)
	mux.HandleFunc("/api/v1/templates", s.templatesHandler)
	mux.HandleFunc("/api/v1/templates/", s.templateHandler)
	mux.HandleFunc("/api/v1/template-catalog", s.templateCatalogHandler)
	mux.HandleFunc("/api/v1/template-catalog/refresh", s.templateCatalogRefreshHandler)
	mux.HandleFunc("/api/v1/provisioning/jobs/", s.provisioningJobHandler)
	mux.HandleFunc("/api/v1/server-update-jobs/", s.serverUpdateJobHandler)
	mux.HandleFunc("/api/v1/node/info", s.nodeInfoHandler)
	mux.HandleFunc("/api/v1/node/health", s.nodeHealthHandler)
	mux.HandleFunc("/api/v1/node/capabilities", s.nodeCapabilitiesHandler)
	mux.HandleFunc("/api/v1/node/enroll", s.nodeEnrollHandler)
	mux.HandleFunc("/api/v1/node/pairing-tokens", s.nodePairingTokensHandler)
	mux.HandleFunc("/api/v1/remote-nodes", s.remoteNodesHandler)
	mux.HandleFunc("/api/v1/remote-nodes/", s.remoteNodeHandler)
	mux.Handle("/", static)
	return s.logRequests(mux)
}

type credentials struct {
	Username        string `json:"username"`
	Email           string `json:"email"`
	Password        string `json:"password"`
	PrepareSteamCMD bool   `json:"prepare_steamcmd"`
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
	values, e := s.settings.Get(r.Context())
	if e != nil {
		internal(w)
		return
	}
	response := map[string]any{"setup_required": required, "password_policy": values.Security, "branding": values.Branding}
	if required && s.setupConfig != nil {
		data, database := s.setupConfig.Storage()
		response["storage"] = map[string]string{"data_directory": data, "database_path": database}
	}
	jsonOut(w, http.StatusOK, response)
}
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		method(w)
		return
	}
	if !s.sameOrigin(r) {
		forbidden(w, "cross-origin request rejected")
		return
	}
	var in credentials
	if !decode(w, r, &in) {
		return
	}
	u, e := s.auth.CreateInitialAdmin(r.Context(), in.Username, in.Email, in.Password)
	if e != nil {
		s.log.With("module", "Auth.Setup", "category", logging.CategoryAuth).Warn("initial administrator creation failed", "reason", e.Error())
		bad(w, "initial setup could not be completed")
		return
	}
	s.log.With("module", "Auth.Setup", "category", logging.CategoryAuth).Info("initial administrator created", "user_id", u.ID)
	if in.PrepareSteamCMD {
		s.startSteamCMDBootstrap()
	}
	s.issueLogin(w, r, u, in.Password)
}

func (s *Server) setupConfigHandler(w http.ResponseWriter, r *http.Request) {
	if s.setupConfig == nil {
		notFound(w)
		return
	}
	required, err := s.auth.SetupRequired(r.Context())
	if err != nil {
		internal(w)
		return
	}
	if !required {
		forbidden(w, "initial setup has already been completed")
		return
	}
	switch r.Method {
	case http.MethodGet:
		data, database := s.setupConfig.Storage()
		jsonOut(w, http.StatusOK, map[string]string{"data_directory": data, "database_path": database})
	case http.MethodPatch:
		if !s.sameOrigin(r) {
			forbidden(w, "cross-origin request rejected")
			return
		}
		var in struct {
			DataDirectory string `json:"data_directory"`
			DatabasePath  string `json:"database_path"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := s.setupConfig.SetStorage(in.DataDirectory, in.DatabasePath); err != nil {
			bad(w, "configuration paths must be absolute and writable")
			return
		}
		jsonOut(w, http.StatusOK, map[string]bool{"restart_required": true})
	default:
		method(w)
	}
}

func (s *Server) setupSteamCMDStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	u, _, ok := s.requireAuth(w, r, false)
	if !ok || !u.IsAdmin {
		if ok {
			forbidden(w, "administrator access required")
		}
		return
	}
	s.bootstrapMu.Lock()
	state := s.bootstrap
	s.bootstrapMu.Unlock()
	jsonOut(w, http.StatusOK, state)
}

func (s *Server) startSteamCMDBootstrap() {
	if s.steamcmd == nil {
		return
	}
	s.bootstrapMu.Lock()
	if s.bootstrap.Status == "preparing" || s.bootstrap.Status == "ready" {
		s.bootstrapMu.Unlock()
		return
	}
	s.bootstrap = bootstrapStatus{Status: "preparing", Summary: "Downloading and preparing SteamCMD"}
	s.bootstrapMu.Unlock()
	go func() {
		err := s.steamcmd.Ensure(context.Background(), func(event steamcmd.Event) {
			s.bootstrapMu.Lock()
			s.bootstrap = bootstrapStatus{Status: "preparing", Summary: event.Summary}
			s.bootstrapMu.Unlock()
		})
		s.bootstrapMu.Lock()
		defer s.bootstrapMu.Unlock()
		if err != nil {
			s.bootstrap = bootstrapStatus{Status: "failed", Summary: "SteamCMD could not be prepared"}
			s.log.With("module", "SteamCMD.Setup", "category", logging.CategorySteamCMD).Error("SteamCMD bootstrap failed", "error", err.Error())
			return
		}
		s.bootstrap = bootstrapStatus{Status: "ready", Summary: "SteamCMD is ready"}
	}()
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		method(w)
		return
	}
	if !s.sameOrigin(r) {
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
			s.log.With("module", "Auth.Login", "category", logging.CategoryAuth).Warn("failed login", "source_ip", r.RemoteAddr)
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
	s.log.With("module", "Auth.Login", "category", logging.CategoryAuth).Info("user logged in", "user_id", u.ID)
	capabilities, err := s.globalCapabilities(ctx, u)
	if err != nil {
		internal(w)
		return
	}
	settingsValues, err := s.settings.Get(ctx)
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"user": u, "csrf_token": csrf, "capabilities": capabilities, "password_policy": settingsValues.Security, "branding": settingsValues.Branding})
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
	settingsValues, err := s.settings.Get(r.Context())
	if err != nil {
		internal(w)
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"user": u, "csrf_token": csrf, "capabilities": capabilities, "password_policy": settingsValues.Security, "branding": settingsValues.Branding})
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
	s.log.With("module", "Auth.Logout", "category", logging.CategoryAuth).Info("user logged out", "user_id", u.ID)
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

var productPermissions = []string{"Server.View", "Server.Create", "Server.Edit", "Server.Delete", "Server.Start", "Server.Stop", "Server.Restart", "Server.Kill", "Server.Update", "Console.View", "Console.Send", "Files.View", "Files.Edit", "Files.Upload", "Files.Download", "Files.Delete", "Files.Rename", "Ports.View", "Ports.Manage", "Users.View", "Users.Manage", "Groups.View", "Groups.Manage", "Roles.View", "Roles.Manage", "Settings.View", "Settings.Manage", "Log.Read", "Log.FlushDirectory", "Templates.View", "Templates.Manage", "Monitoring.View", "Audit.View", "Tenants.View", "Tenants.Manage", "Node.View", "Node.Manage"}

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
	names, err := s.tenantNames(ctx)
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
		result = append(result, map[string]any{"server": record.Server, "runtime": record.Runtime, "capabilities": capabilities, "tenant_name": names[record.Server.TenantID]})
	}
	return result, nil
}

// tenantNames resolves every tenant ID to its display name in one query, for
// server list/detail responses that add a safe tenant_name alongside the
// server's own tenant_id (never a storage root or host path). A missing
// entry - a server pointing at a tenant that no longer exists, which should
// not normally happen - resolves to the zero value "" rather than an error.
func (s *Server) tenantNames(ctx context.Context) (map[string]string, error) {
	list, err := s.tenants.List(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(list))
	for _, tenant := range list {
		names[tenant.ID] = tenant.Name
	}
	return names, nil
}

// tenantName resolves a single tenant ID for one-record server responses
// (create/update/detail). A failed lookup is treated as "unknown" (empty
// name) rather than failing the whole request.
func (s *Server) tenantName(ctx context.Context, tenantID string) string {
	tenant, err := s.tenants.Get(ctx, tenantID)
	if err != nil {
		return ""
	}
	return tenant.Name
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
	if csrfRequired && (!s.sameOrigin(r) || r.Header.Get("X-CSRF-Token") != csrf) {
		forbidden(w, "csrf validation failed")
		return auth.User{}, "", false
	}
	return u, csrf, true
}
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	scheme := "http"
	host := r.Host
	if r.TLS != nil {
		scheme = "https"
	} else if s.trustLocalProxy && isLoopbackPeer(r.RemoteAddr) {
		if forwardedScheme := singleForwardedValue(r.Header.Get("X-Forwarded-Proto")); forwardedScheme == "http" || forwardedScheme == "https" {
			scheme = forwardedScheme
		}
		if forwardedHost := singleForwardedValue(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
			host = forwardedHost
		}
	}
	return e == nil && u.Host == host && u.Scheme == scheme
}

func isLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func singleForwardedValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, ",") {
		return ""
	}
	return value
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

// logRequests records exactly one line per HTTP request: method, path
// (stripped of any query string), status, response size, and duration -
// never headers, cookies, or the request/response body, so it can never
// leak Authorization headers, CSRF tokens, session cookies, or submitted
// credentials. Routine successful traffic is logged at debug so the default
// info level isn't flooded by normal API/browser polling; genuine HTTP
// errors (4xx/5xx) always remain visible at warn/error regardless of level
// or the http category setting below.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		tracked := &responseLogWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(tracked, r)
		args := []any{"module", "HTTP", "category", logging.CategoryHTTP, "method", r.Method, "path", strings.Split(r.URL.Path, "?")[0], "status", tracked.status, "response_bytes", tracked.bytes, "duration", time.Since(start).String()}
		switch {
		case tracked.status >= 500:
			s.log.Error("http request failed", args...)
		case tracked.status >= 400:
			s.log.Warn("http request rejected", args...)
		default:
			s.log.Debug("http request completed", args...)
		}
	})
}

type responseLogWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *responseLogWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseLogWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}
func (w *responseLogWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
func (w *responseLogWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}
func (w *responseLogWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}
func (w *responseLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
