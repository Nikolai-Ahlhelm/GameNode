package api

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"gamenode"
	"gamenode/internal/auth"
	"gamenode/internal/console"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"gamenode/internal/rbac"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
)

type consoleInput struct {
	mu   sync.Mutex
	data string
}

func (i *consoleInput) Write(p []byte) (int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.data += string(p)
	return len(p), nil
}
func (i *consoleInput) Close() error { return nil }
func (i *consoleInput) String() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.data
}

func consoleFixture(t *testing.T) (*httptest.Server, *servers.Service, *console.Manager, *http.Cookie, servers.Record, *sql.DB) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := auth.New(db)
	u, err := a.CreateInitialAdmin(context.Background(), "admin", "admin@example.test", "a password long enough")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := a.CreateSession(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	m := console.NewManager()
	svc := servers.NewService(servers.NewStore(db), runtime.NewNative(), m)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.Create(context.Background(), servers.Server{Name: "test", CreationMode: servers.CreationCustom, WorkingDirectory: filepath.Dir(exe), Executable: exe, EnvironmentVariables: map[string]string{}, StopTimeoutSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(New(a, svc, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)), false).Handler(http.NotFoundHandler()))
	t.Cleanup(h.Close)
	return h, svc, m, &http.Cookie{Name: "gamenode_session", Value: raw}, r, db
}
func consoleURL(base, id string) string {
	u, _ := url.Parse(base)
	u.Scheme = "ws"
	u.Path = "/api/v1/servers/" + id + "/console/ws"
	return u.String()
}
func dialConsole(t *testing.T, base, id string, c *http.Cookie, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	if c != nil {
		h.Add("Cookie", c.String())
	}
	if origin != "" {
		h.Set("Origin", origin)
	}
	return websocket.DefaultDialer.Dial(consoleURL(base, id), h)
}
func readConsole(t *testing.T, c *websocket.Conn) consoleMessage {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var m consoleMessage
	if err := c.ReadJSON(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestConsoleWebSocketSecurityAndTransport(t *testing.T) {
	h, _, m, cookie, record, db := consoleFixture(t)
	if _, _, err := dialConsole(t, h.URL, record.Server.ID, nil, h.URL); err == nil {
		t.Fatal("unauthenticated websocket accepted")
	}
	if _, resp, err := dialConsole(t, h.URL, record.Server.ID, cookie, "http://attacker.test"); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatal("cross-origin websocket accepted")
	}
	if _, resp, err := dialConsole(t, h.URL, "missing", cookie, h.URL); err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatal("unknown server accepted")
	}
	in := &consoleInput{}
	session := m.Start(record.Server.ID, in)
	session.Publish("stdout", "history")
	first, _, err := dialConsole(t, h.URL, record.Server.ID, cookie, h.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if got := readConsole(t, first); got.Type != "console" || got.State != "attached" {
		t.Fatalf("state %#v", got)
	}
	if got := readConsole(t, first); got.Stream != "stdout" || got.Data != "history" {
		t.Fatalf("history %#v", got)
	}
	_ = readConsole(t, first)
	second, _, err := dialConsole(t, h.URL, record.Server.ID, cookie, h.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = readConsole(t, second)
	_ = readConsole(t, second)
	_ = readConsole(t, second)
	session.Publish("stdout", "live-out")
	session.Publish("stderr", "live-err")
	if got := readConsole(t, first); got.Data != "live-out" {
		t.Fatalf("live stdout %#v", got)
	}
	if got := readConsole(t, first); got.Stream != "stderr" {
		t.Fatalf("live stderr %#v", got)
	}
	if got := readConsole(t, second); got.Data != "live-out" {
		t.Fatal("second client missed output")
	}
	const inputSecret = "AUDIT_CONSOLE_SECRET_SHOULD_NEVER_APPEAR"
	input := inputSecret + "\n"
	if err := first.WriteJSON(consoleClientMessage{Type: "input", Data: input}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for in.String() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := in.String(); got != input {
		t.Fatalf("input = %q", got)
	}
	var actorID, serverID, metadata, resourceName, summary string
	auditDeadline := time.Now().Add(time.Second)
	for {
		err = db.QueryRow(`SELECT actor_user_id,server_id,COALESCE(metadata_json,''),COALESCE(resource_name,''),COALESCE(error_summary,'') FROM audit_log WHERE action='console.input' AND result='success'`).Scan(&actorID, &serverID, &metadata, &resourceName, &summary)
		if err == nil {
			break
		}
		if err != sql.ErrNoRows || time.Now().After(auditDeadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if actorID == "" || serverID != record.Server.ID || !strings.Contains(metadata, `"bytes":`+strconv.Itoa(len([]byte(input)))) {
		t.Fatalf("unexpected console audit event: actor=%q server=%q metadata=%q", actorID, serverID, metadata)
	}
	if strings.Contains(actorID+serverID+metadata+resourceName+summary, inputSecret) {
		t.Fatal("console input leaked into audit event")
	}
}

func TestConsoleWebSocketDetachedAndClosed(t *testing.T) {
	h, _, m, cookie, record, _ := consoleFixture(t)
	m.MarkDetached(record.Server.ID)
	c, _, err := dialConsole(t, h.URL, record.Server.ID, cookie, h.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := readConsole(t, c); got.State != "detached" {
		t.Fatalf("detached %#v", got)
	}
	c.Close()
}

func TestConsoleWebSocketMalformedAndOversizedInput(t *testing.T) {
	h, _, m, cookie, record, _ := consoleFixture(t)
	in := &consoleInput{}
	session := m.Start(record.Server.ID, in)
	for _, payload := range [][]byte{[]byte("{bad"), []byte(`{"type":"input","data":"` + string(make([]byte, console.MaxInputBytes+2048)) + `"}`)} {
		client, _, err := dialConsole(t, h.URL, record.Server.ID, cookie, h.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = readConsole(t, client)
		_ = client.WriteMessage(websocket.TextMessage, payload)
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		_, _, _ = client.ReadMessage()
		client.Close()
	}
	if got := in.String(); got != "" {
		t.Fatalf("invalid input reached stdin: %q", got)
	}
	if err := session.Input("ok\n"); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleWebSocketClosedState(t *testing.T) {
	h, _, m, cookie, record, _ := consoleFixture(t)
	session := m.Start(record.Server.ID, &consoleInput{})
	session.Close("stopped")
	m.ClearCurrentSession(record.Server.ID, session.ID)
	client, _, err := dialConsole(t, h.URL, record.Server.ID, cookie, h.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if got := readConsole(t, client); got.Type != "console" || got.State != "closed" {
		t.Fatalf("closed state %#v", got)
	}
}

func TestConsoleRBACViewAndSendAreIndependent(t *testing.T) {
	h, _, manager, _, record, db := consoleFixture(t)
	ctx := context.Background()
	member, err := identity.New(db).CreateUser(ctx, identity.CreateUserInput{Username: "viewer", Email: "viewer@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := auth.New(db).CreateSession(ctx, auth.User{ID: member.ID})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "gamenode_session", Value: raw}
	if _, _, err := dialConsole(t, h.URL, record.Server.ID, cookie, h.URL); err == nil {
		t.Fatal("Console.View absent but websocket accepted")
	}
	role, err := rbac.New(db).CreateRole(ctx, "console-view", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = rbac.New(db).ReplacePermissions(ctx, role.ID, []string{"Console.View"}); err != nil {
		t.Fatal(err)
	}
	if err = rbac.New(db).AssignUser(ctx, member.ID, role.ID, rbac.Scope{Type: "server", ID: &record.Server.ID}); err != nil {
		t.Fatal(err)
	}
	in := &consoleInput{}
	manager.Start(record.Server.ID, in)
	client, _, err := dialConsole(t, h.URL, record.Server.ID, cookie, h.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if state := readConsole(t, client); state.State != "attached" {
		t.Fatalf("view state: %#v", state)
	}
	if err := client.WriteJSON(consoleClientMessage{Type: "input", Data: "denied\n"}); err != nil {
		t.Fatal(err)
	}
	denied := false
	for range 3 { // attached state may be followed by the runtime state before the input error.
		message := readConsole(t, client)
		if message.Type == "error" && message.State == "permission_denied" {
			denied = true
			break
		}
	}
	if !denied {
		t.Fatal("view-only input did not return permission_denied")
	}
	if got := in.String(); got != "" {
		t.Fatalf("view-only input reached stdin: %q", got)
	}
	if err = rbac.New(db).ReplacePermissions(ctx, role.ID, []string{"Console.View", "Console.Send"}); err != nil {
		t.Fatal(err)
	}
	if err := client.WriteJSON(consoleClientMessage{Type: "input", Data: "allowed\n"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for in.String() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := in.String(); got != "allowed\n" {
		t.Fatalf("Console.Send input = %q", got)
	}
}
