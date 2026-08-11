package api

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"

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

func (s *Server) consoleWS(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	u, _, ok := s.requireServerPermission(w, r, "Console.View", id, false)
	if !ok {
		return
	}
	if !sameOrigin(r) {
		forbidden(w, "cross-origin request rejected")
		return
	}
	record, err := s.servers.Get(r.Context(), id)
	if err != nil {
		serverError(w, err, false)
		return
	}
	_ = record
	upgrader := consoleUpgrader
	upgrader.CheckOrigin = sameOrigin
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(console.MaxInputBytes + 1024)
	manager := s.servers.Console()
	session, attached := manager.CurrentSession(id)
	if !attached {
		state := "closed"
		if manager.IsDetached(id) {
			state = "detached"
		}
		writeConsole(conn, consoleMessage{Type: "console", State: state})
		return
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
				select {
				case errors <- consoleMessage{Type: "error", State: "permission_denied"}:
				default:
				}
				continue
			}
			if err := session.Input(in.Data); err != nil {
				select {
				case errors <- consoleMessage{Type: "error", State: "input_unavailable"}:
				default:
				}
			}
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
