package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/console"
	"gamenode/internal/rbac"
)

const consoleWriteWait = 5 * time.Second

var consoleUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return false }}

type consoleClientMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
}
type consoleMessage struct {
	Type      string    `json:"type"`
	Stream    string    `json:"stream,omitempty"`
	Data      string    `json:"data,omitempty"`
	State     string    `json:"state,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

func (s *Server) recordConsoleInputAudit(r *http.Request, actor auth.User, serverID, result string, bytes int, errorCode, errorSummary string) {
	server := serverID
	metadata, _ := json.Marshal(map[string]int{"bytes": bytes})
	s.recordAudit(r, auditInput{action: audit.ConsoleInput, resourceType: audit.Console, serverID: &server, result: result, metadata: metadata, errorCode: errorCode, errorSummary: errorSummary, actor: &actor})
}

func (s *Server) consoleWS(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, "Console.View", id, false)
	if !ok {
		return
	}
	if !s.sameOrigin(r) {
		forbidden(w, "cross-origin request rejected")
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		s.log.Error("console connection failed while loading server", "module", "Console.Connection", "server_id", id, "error", err)
		serverError(w, err, false)
		return
	}
	_ = record
	upgrader := consoleUpgrader
	upgrader.CheckOrigin = s.sameOrigin
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("console websocket upgrade failed", "module", "Console.Connection", "server_id", id, "user_id", u.ID, "error", err)
		return
	}
	s.log.Info("console client connected", "module", "Console.Connection", "server_id", id, "user_id", u.ID)
	defer s.log.Info("console client disconnected", "module", "Console.Connection", "server_id", id, "user_id", u.ID)
	defer conn.Close()
	conn.SetReadLimit(console.MaxInputBytes + 1024)
	manager := s.servers.Console()
	session, attached := manager.CurrentSession(id)
	if !attached {
		if manager.IsDetached(id) {
			s.log.Info("console client received detached state", "module", "Console.Connection", "server_id", id, "user_id", u.ID)
			writeConsole(conn, consoleMessage{Type: "console", State: "detached"})
			return
		}
		session, attached = manager.LastClosedSession(id)
		if !attached {
			s.log.Info("console client received closed state", "module", "Console.Connection", "server_id", id, "user_id", u.ID)
			writeConsole(conn, consoleMessage{Type: "console", State: "closed"})
			return
		}
	}
	if !writeConsole(conn, consoleMessage{Type: "console", State: "attached"}) {
		return
	}
	events, unsubscribe := session.Subscribe()
	defer unsubscribe()
	inputDone := make(chan struct{})
	errors := make(chan consoleMessage, 1)
	go func() {
		defer close(inputDone)
		for {
			var in consoleClientMessage
			if err := conn.ReadJSON(&in); err != nil {
				s.log.Debug("console websocket read ended", "module", "Console.Connection", "server_id", id, "user_id", u.ID, "error", err)
				return
			}
			if in.Type != "input" || in.Data == "" {
				select {
				case errors <- consoleMessage{Type: "error", State: "invalid_input"}:
				default:
				}
				continue
			}
			canSend, err := s.allowed(r.Context(), u, "Console.Send", rbac.Scope{Type: "server", ID: &id})
			if err != nil || !canSend {
				s.log.Warn("console input rejected", "module", "Console.Input", "server_id", id, "user_id", u.ID, "bytes", len([]byte(in.Data)), "error", err)
				s.recordConsoleInputAudit(r, u, id, audit.Failure, len([]byte(in.Data)), "permission_denied", "console input permission denied")
				select {
				case errors <- consoleMessage{Type: "error", State: "permission_denied"}:
				default:
				}
				continue
			}
			if err := session.Input(in.Data); err != nil {
				s.log.Error("console input delivery failed", "module", "Console.Input", "server_id", id, "user_id", u.ID, "bytes", len([]byte(in.Data)), "error", err)
				s.recordConsoleInputAudit(r, u, id, audit.Failure, len([]byte(in.Data)), "input_unavailable", "console input is unavailable")
				select {
				case errors <- consoleMessage{Type: "error", State: "input_unavailable"}:
				default:
				}
				continue
			}
			s.recordConsoleInputAudit(r, u, id, audit.Success, len([]byte(in.Data)), "", "")
			s.log.Info("console input delivered", "module", "Console.Input", "server_id", id, "user_id", u.ID, "bytes", len([]byte(in.Data)))
		}
	}()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			canView, err := s.allowed(r.Context(), u, "Console.View", rbac.Scope{Type: "server", ID: &id})
			if err != nil || !canView {
				return
			}
			if !writeConsole(conn, consoleMessage{Type: event.Type, Stream: event.Stream, Data: event.Data, State: event.State, Timestamp: event.Timestamp}) {
				return
			}
		case message := <-errors:
			if !writeConsole(conn, message) {
				return
			}
		case <-inputDone:
			return
		}
	}
}

func writeConsole(conn *websocket.Conn, message consoleMessage) bool {
	_ = conn.SetWriteDeadline(time.Now().Add(consoleWriteWait))
	return conn.WriteJSON(message) == nil
}
