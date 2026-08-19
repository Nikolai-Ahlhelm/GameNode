package audit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	MaxMetadataBytes = 4096
	DefaultLimit     = 100
	MaxLimit         = 500
)
const (
	Success = "success"
	Failure = "failure"
)
const (
	Auth     = "auth"
	Server   = "server"
	Console  = "console"
	File     = "file"
	User     = "user"
	Group    = "group"
	Role     = "role"
	Port     = "port"
	Settings = "settings"
	System   = "system"
	Template = "template"
	Tenant   = "tenant"
	Node     = "node"
)
const (
	Login                        = "auth.login"
	Logout                       = "auth.logout"
	ServerCreate                 = "server.create"
	ServerUpdate                 = "server.update"
	ServerDelete                 = "server.delete"
	ServerStart                  = "server.start"
	ServerStop                   = "server.stop"
	ServerRestart                = "server.restart"
	ServerRestartScheduleCreate  = "server.restart_schedule_create"
	ServerRestartScheduleUpdate  = "server.restart_schedule_update"
	ServerRestartScheduleEnable  = "server.restart_schedule_enable"
	ServerRestartScheduleDisable = "server.restart_schedule_disable"
	ServerRestartScheduleDelete  = "server.restart_schedule_delete"
	ServerKill                   = "server.kill"
	PortCreate                   = "port.create"
	PortUpdate                   = "port.update"
	PortDelete                   = "port.delete"
	FileCreate                   = "file.create"
	FileEdit                     = "file.edit"
	FileRename                   = "file.rename"
	FileMove                     = "file.move"
	FileDelete                   = "file.delete"
	FileUpload                   = "file.upload"
	ConsoleInput                 = "console.input"
	UserCreate                   = "user.create"
	UserUpdate                   = "user.update"
	UserEnable                   = "user.enable"
	UserDisable                  = "user.disable"
	UserDelete                   = "user.delete"
	UserPasswordReset            = "user.password_reset"
	GroupCreate                  = "group.create"
	GroupUpdate                  = "group.update"
	GroupDelete                  = "group.delete"
	GroupMemberAdd               = "group.member_add"
	GroupMemberRemove            = "group.member_remove"
	RoleCreate                   = "role.create"
	RoleUpdate                   = "role.update"
	RoleDelete                   = "role.delete"
	RolePermissionsUpdate        = "role.permissions_update"
	RoleAssignmentAdd            = "role.assignment_add"
	RoleAssignmentRemove         = "role.assignment_remove"
	SettingsUpdate               = "settings.update"
	SettingsLogsClear            = "settings.logs_clear"
	SupportBundleGenerate        = "support.bundle_generate"
	TemplateImport               = "template.import"
	TemplateDelete               = "template.delete"
	ServerProvisionStart         = "server.provision_start"
	ServerProvisionRetry         = "server.provision_retry"
	ServerProvisionComplete      = "server.provision_complete"
	ServerProvisionFail          = "server.provision_fail"
	ServerProvisionCancel        = "server.provision_cancel"
	// SteamCMD server-update actions (v0.2.1) are deliberately distinct from
	// ServerUpdate above, which records ordinary server definition edits.
	// These record the manual "update installed Steam depot" workflow (see
	// internal/serverupdates): one event when a job starts, one terminal
	// result (complete/fail), and one on cancellation.
	ServerSteamCMDUpdateStart    = "server.steamcmd_update_start"
	ServerSteamCMDUpdateComplete = "server.steamcmd_update_complete"
	ServerSteamCMDUpdateFail     = "server.steamcmd_update_fail"
	ServerSteamCMDUpdateCancel   = "server.steamcmd_update_cancel"
	// Tenant actions are catalogued here for the Tenant Foundation domain
	// (internal/tenants) ahead of its API layer. No handler records these yet;
	// see docs/architecture.md and AGENTS.md's audit rules on best-effort,
	// actor-attributed recording once that transport exists.
	TenantCreate       = "tenant.create"
	TenantUpdate       = "tenant.update"
	TenantDelete       = "tenant.delete"
	TenantMemberAdd    = "tenant.member_add"
	TenantMemberRemove = "tenant.member_remove"
	// Node actions cover the Remote Node Foundation (v0.5A): enrolling a
	// remote node into this controller, changing its registry entry, and
	// generating a pairing token so ANOTHER controller can enroll THIS
	// node. Heartbeats/health polls are deliberately not audited (see
	// AGENTS.md item 26).
	NodePairingTokenCreate = "node.pairing_token_create"
	NodeEnroll             = "node.enroll"
	NodeUpdate             = "node.update"
	NodeEnable             = "node.enable"
	NodeDisable            = "node.disable"
	NodeRemove             = "node.remove"
)

type Event struct {
	ID            string          `json:"id"`
	Timestamp     time.Time       `json:"timestamp"`
	ActorUserID   *string         `json:"actor_user_id,omitempty"`
	ActorUsername string          `json:"actor_username,omitempty"`
	Action        string          `json:"action"`
	ResourceType  string          `json:"resource_type"`
	ResourceID    *string         `json:"resource_id,omitempty"`
	ResourceName  string          `json:"resource_name,omitempty"`
	ServerID      *string         `json:"server_id,omitempty"`
	Result        string          `json:"result"`
	RemoteIP      string          `json:"remote_ip,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	ErrorCode     string          `json:"error_code,omitempty"`
	ErrorSummary  string          `json:"error_summary,omitempty"`
}
type Filter struct {
	ActorUserID          *string
	Action, ResourceType string
	Query                string
	ResourceID, ServerID *string
	Result               string
	Limit                int
	Offset               int
}
type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db} }
func (s *Service) Record(c context.Context, e Event) error {
	if e.Action == "" || e.ResourceType == "" || (e.Result != Success && e.Result != Failure) {
		return errors.New("invalid audit event")
	}
	if len(e.Metadata) > MaxMetadataBytes {
		return errors.New("audit metadata too large")
	}
	if len(e.Metadata) > 0 && !json.Valid(e.Metadata) {
		return errors.New("audit metadata must be JSON")
	}
	if e.ID == "" {
		e.ID = id()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	_, x := s.db.ExecContext(c, "INSERT INTO audit_log(id,timestamp,actor_user_id,actor_username,action,resource_type,resource_id,resource_name,server_id,result,remote_ip,metadata_json,error_code,error_summary) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", e.ID, e.Timestamp.Format(time.RFC3339Nano), e.ActorUserID, e.ActorUsername, e.Action, e.ResourceType, e.ResourceID, e.ResourceName, e.ServerID, e.Result, e.RemoteIP, nullable(e.Metadata), e.ErrorCode, e.ErrorSummary)
	return x
}
func (s *Service) List(c context.Context, f Filter) ([]Event, error) {
	if len(f.Query) > 100 {
		return nil, errors.New("audit query too long")
	}
	if f.Limit <= 0 {
		f.Limit = DefaultLimit
	}
	if f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}
	q := "SELECT id,timestamp,actor_user_id,actor_username,action,resource_type,resource_id,resource_name,server_id,result,remote_ip,metadata_json,error_code,error_summary FROM audit_log WHERE 1=1"
	a := []any{}
	if f.ActorUserID != nil {
		q += " AND actor_user_id=?"
		a = append(a, *f.ActorUserID)
	}
	if f.Action != "" {
		q += " AND action=?"
		a = append(a, f.Action)
	}
	if f.ResourceType != "" {
		q += " AND resource_type=?"
		a = append(a, f.ResourceType)
	}
	if f.ResourceID != nil {
		q += " AND resource_id=?"
		a = append(a, *f.ResourceID)
	}
	if f.ServerID != nil {
		q += " AND server_id=?"
		a = append(a, *f.ServerID)
	}
	if f.Result != "" {
		q += " AND result=?"
		a = append(a, f.Result)
	}
	if strings.TrimSpace(f.Query) != "" {
		q += ` AND (action LIKE ? ESCAPE '\' OR actor_username LIKE ? ESCAPE '\' OR resource_type LIKE ? ESCAPE '\' OR resource_name LIKE ? ESCAPE '\' OR COALESCE(resource_id,'') LIKE ? ESCAPE '\' OR COALESCE(server_id,'') LIKE ? ESCAPE '\' OR error_code LIKE ? ESCAPE '\' OR error_summary LIKE ? ESCAPE '\')`
		pattern := auditSearchPattern(strings.TrimSpace(f.Query))
		for range 8 {
			a = append(a, pattern)
		}
	}
	q += " ORDER BY timestamp DESC,id DESC LIMIT ? OFFSET ?"
	a = append(a, f.Limit, f.Offset)
	rows, e := s.db.QueryContext(c, q, a...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var x Event
		var ts string
		var actor, res, server, meta sql.NullString
		if e = rows.Scan(&x.ID, &ts, &actor, &x.ActorUsername, &x.Action, &x.ResourceType, &res, &x.ResourceName, &server, &x.Result, &x.RemoteIP, &meta, &x.ErrorCode, &x.ErrorSummary); e != nil {
			return nil, e
		}
		x.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		if actor.Valid {
			x.ActorUserID = &actor.String
		}
		if res.Valid {
			x.ResourceID = &res.String
		}
		if server.Valid {
			x.ServerID = &server.String
		}
		if meta.Valid {
			x.Metadata = json.RawMessage(meta.String)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func auditSearchPattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return "%" + value + "%"
}
func nullable(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
func id() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return strings.ToLower(hex.EncodeToString(b))
}
