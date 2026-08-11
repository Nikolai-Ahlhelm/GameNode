//go:build ignore

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	rootDir = "I:/GameNode"
	baseURL = "http://127.0.0.1:18765"
)

type consoleEvent struct{ Type, Stream, Data, State string }

type runtimeState struct {
	PID            int        `json:"pid"`
	ProcessStartAt *time.Time `json:"process_started_at"`
	CurrentState   string     `json:"current_state"`
}

type serverRecord struct {
	Runtime runtimeState `json:"runtime"`
}

type wsClient struct {
	conn   *websocket.Conn
	events chan consoleEvent
	errs   chan error
	done   chan struct{}
	close  sync.Once
	write  sync.Mutex
	seen   []consoleEvent
}

func newWSClient(endpoint string, headers http.Header) *wsClient {
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, headers)
	if err != nil {
		panic(err)
	}
	c := &wsClient{conn: conn, events: make(chan consoleEvent, 256), errs: make(chan error, 1), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		defer close(c.events)
		for {
			var event consoleEvent
			if err := conn.ReadJSON(&event); err != nil {
				select {
				case c.errs <- err:
				default:
				}
				return
			}
			c.events <- event
		}
	}()
	return c
}

func (c *wsClient) Close() { c.close.Do(func() { _ = c.conn.Close(); <-c.done }) }
func (c *wsClient) Send(event consoleEvent) {
	c.write.Lock()
	defer c.write.Unlock()
	if err := c.conn.WriteJSON(event); err != nil {
		panic(err)
	}
}
func (c *wsClient) Wait(ctx context.Context, predicate func(consoleEvent) bool) {
	for {
		// Prefer already received events over a terminal connection error. A closed
		// console endpoint writes its final state before closing the WebSocket.
		select {
		case event, ok := <-c.events:
			if !ok {
				panic("websocket closed")
			}
			c.seen = append(c.seen, event)
			if predicate(event) {
				return
			}
			continue
		default:
		}

		select {
		case event, ok := <-c.events:
			if !ok {
				panic("websocket closed")
			}
			c.seen = append(c.seen, event)
			if predicate(event) {
				return
			}
		case err := <-c.errs:
			panic(err)
		case <-ctx.Done():
			panic(fmt.Sprintf("timeout; events=%v", c.seen))
		}
	}
}

func (c *wsClient) WaitForConsoleState(ctx context.Context, state string) {
	c.Wait(ctx, func(event consoleEvent) bool {
		return event.Type == "console" && event.State == state
	})
}

func (c *wsClient) WaitForOutput(ctx context.Context, stream, marker string) {
	c.Wait(ctx, func(event consoleEvent) bool {
		return event.Type == "output" && event.Stream == stream && strings.Contains(event.Data, marker)
	})
}

func post(client *http.Client, path, body, csrf string) map[string]any {
	req, _ := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 300 {
		panic("api " + path)
	}
	defer resp.Body.Close()
	result := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result
}

func put(client *http.Client, path, body, csrf string) {
	req, _ := http.NewRequest(http.MethodPut, baseURL+path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 300 {
		panic("api put " + path)
	}
	resp.Body.Close()
}

func expectStatus(client *http.Client, method, path string, expected int) {
	req, _ := http.NewRequest(method, baseURL+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != expected {
		panic(fmt.Sprintf("%s %s: got %d want %d", method, path, resp.StatusCode, expected))
	}
}

func waitForAPI(client *http.Client) {
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		response, err := client.Get(baseURL + "/api/v1/setup/status")
		if err == nil && response.StatusCode == http.StatusOK {
			response.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			panic("api ready timeout")
		}
	}
}

func getServer(client *http.Client, id string) serverRecord {
	response, err := client.Get(baseURL + "/api/v1/servers/" + id)
	if err != nil {
		panic(fmt.Sprintf("get server: %v", err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		panic(fmt.Sprintf("get server: status %d", response.StatusCode))
	}
	var record serverRecord
	if err := json.NewDecoder(response.Body).Decode(&record); err != nil {
		panic(fmt.Sprintf("decode server: %v", err))
	}
	return record
}

func waitForServerState(ctx context.Context, client *http.Client, id, state string) serverRecord {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var last serverRecord
	for {
		last = getServer(client, id)
		if last.Runtime.CurrentState == state {
			return last
		}
		select {
		case <-ctx.Done():
			panic(fmt.Sprintf("wait for server state %q; last runtime=%+v", state, last.Runtime))
		case <-ticker.C:
		}
	}
}

func sameProcessIdentity(left, right runtimeState) bool {
	if left.PID != right.PID {
		return false
	}
	if left.ProcessStartAt == nil || right.ProcessStartAt == nil {
		return left.ProcessStartAt == right.ProcessStartAt
	}
	return left.ProcessStartAt.Equal(*right.ProcessStartAt)
}

func waitForNewProcessIdentity(ctx context.Context, client *http.Client, id string, previous runtimeState) serverRecord {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var last serverRecord
	for {
		last = getServer(client, id)
		if last.Runtime.CurrentState == "running" && last.Runtime.PID != 0 && !sameProcessIdentity(previous, last.Runtime) {
			return last
		}
		select {
		case <-ctx.Done():
			panic(fmt.Sprintf("wait for new process identity; previous=%+v last=%+v", previous, last.Runtime))
		case <-ticker.C:
		}
	}
}

func connectConsole(endpoint string, headers http.Header) *wsClient {
	return newWSClient(endpoint, headers)
}

func verifyAttachedConsole(ctx context.Context, client *wsClient, input, expectedOutput string) {
	client.WaitForConsoleState(ctx, "attached")
	client.WaitForOutput(ctx, "stdout", "READY")
	client.WaitForOutput(ctx, "stderr", "HELPER_STDERR_READY")
	client.Send(consoleEvent{Type: "input", Data: input})
	client.WaitForOutput(ctx, "stdout", expectedOutput)
}

func main() {
	_ = os.RemoveAll(rootDir + "/tmp-e2e-data")
	node := exec.Command(rootDir+"/dist/gamenode-windows-amd64.exe", "-config", rootDir+"/tmp-e2e-config.yaml")
	if err := node.Start(); err != nil {
		panic(err)
	}
	defer node.Process.Kill()

	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar}
	waitForAPI(httpClient)
	setup := post(httpClient, "/api/v1/setup", `{"username":"e2eadmin","email":"e2e@example.test","password":"a password long enough"}`, "")
	csrf := setup["csrf_token"].(string)
	created := post(httpClient, "/api/v1/servers", fmt.Sprintf(`{"creation_mode":"custom","name":"e2e","working_directory":"%s","executable":"%s/tmp-e2e-helper.exe","arguments":[],"environment_variables":{},"stop_timeout_seconds":2}`, rootDir, rootDir), csrf)
	id := created["server"].(map[string]any)["id"].(string)
	post(httpClient, "/api/v1/servers/"+id+"/start", "", csrf)

	u, _ := url.Parse(baseURL)
	headers := http.Header{"Origin": []string{baseURL}, "Cookie": []string{jar.Cookies(u)[0].String()}}
	endpoint := "ws://127.0.0.1:18765/api/v1/servers/" + id + "/console/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	first := connectConsole(endpoint, headers)
	defer first.Close()
	first.WaitForConsoleState(ctx, "attached")
	first.WaitForOutput(ctx, "stdout", "READY")
	first.WaitForOutput(ctx, "stderr", "HELPER_STDERR_READY")
	first.Send(consoleEvent{Type: "input", Data: "hello\n"})
	first.WaitForOutput(ctx, "stdout", "ECHO:hello")

	second := connectConsole(endpoint, headers)
	defer second.Close()
	second.WaitForConsoleState(ctx, "attached")
	first.Send(consoleEvent{Type: "input", Data: "second\n"})
	first.WaitForOutput(ctx, "stdout", "ECHO:second")
	second.WaitForOutput(ctx, "stdout", "ECHO:second")
	first.Close()
	second.Send(consoleEvent{Type: "input", Data: "after\n"})
	second.WaitForOutput(ctx, "stdout", "ECHO:after")
	fmt.Println("E2E_WEBSOCKET_OK")

	lifecycleCtx, lifecycleCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer lifecycleCancel()
	previous := waitForServerState(lifecycleCtx, httpClient, id, "running")
	post(httpClient, "/api/v1/servers/"+id+"/stop", "", csrf)
	waitForServerState(lifecycleCtx, httpClient, id, "stopped")
	closed := connectConsole(endpoint, headers)
	closed.WaitForConsoleState(lifecycleCtx, "closed")
	closed.Close()
	second.Close()

	post(httpClient, "/api/v1/servers/"+id+"/start", "", csrf)
	started := waitForNewProcessIdentity(lifecycleCtx, httpClient, id, previous.Runtime)
	afterStart := connectConsole(endpoint, headers)
	verifyAttachedConsole(lifecycleCtx, afterStart, "after-start\n", "ECHO:after-start")

	post(httpClient, "/api/v1/servers/"+id+"/restart", "", csrf)
	waitForNewProcessIdentity(lifecycleCtx, httpClient, id, started.Runtime)
	afterRestart := connectConsole(endpoint, headers)
	defer afterRestart.Close()
	verifyAttachedConsole(lifecycleCtx, afterRestart, "after-restart\n", "ECHO:after-restart")
	afterStart.Close()

	fmt.Println("E2E_MILESTONE3_OK")

	// RBAC product enforcement uses a separate non-admin session against the
	// same real server and Windows runtime process.
	user := post(httpClient, "/api/v1/users", `{"username":"rbacuser","email":"rbac@example.test","password":"a password long enough"}`, csrf)
	userID := user["user"].(map[string]any)["id"].(string)
	userJar, _ := cookiejar.New(nil)
	userClient := &http.Client{Jar: userJar}
	post(userClient, "/api/v1/auth/login", `{"username":"rbacuser","password":"a password long enough"}`, "")
	expectStatus(userClient, http.MethodGet, "/api/v1/servers", http.StatusOK)
	role := post(httpClient, "/api/v1/roles", `{"name":"e2e-rbac","description":"rbac e2e"}`, csrf)
	roleID := role["role"].(map[string]any)["id"].(string)
	put(httpClient, "/api/v1/roles/"+roleID+"/permissions", `{"permissions":["Server.View","Console.View"]}`, csrf)
	post(httpClient, "/api/v1/users/"+userID+"/roles", fmt.Sprintf(`{"role_id":%q,"scope_type":"server","scope_id":%q}`, roleID, id), csrf)
	expectStatus(userClient, http.MethodGet, "/api/v1/servers/"+id, http.StatusOK)
	memberHeaders := http.Header{"Origin": []string{baseURL}, "Cookie": []string{userJar.Cookies(u)[0].String()}}
	viewer := connectConsole(endpoint, memberHeaders)
	viewer.WaitForConsoleState(lifecycleCtx, "attached")
	viewer.Send(consoleEvent{Type: "input", Data: "denied\n"})
	viewer.Wait(lifecycleCtx, func(event consoleEvent) bool { return event.Type == "error" && event.State == "permission_denied" })
	put(httpClient, "/api/v1/roles/"+roleID+"/permissions", `{"permissions":["Server.View","Console.View","Console.Send","Files.View"]}`, csrf)
	viewer.Send(consoleEvent{Type: "input", Data: "rbac\n"})
	viewer.WaitForOutput(lifecycleCtx, "stdout", "ECHO:rbac")
	expectStatus(userClient, http.MethodGet, "/api/v1/servers/"+id+"/files", http.StatusOK)
	expectStatus(userClient, http.MethodGet, "/api/v1/servers/"+id+"/files/download?path=tmp-e2e-helper.exe", http.StatusForbidden)
	put(httpClient, "/api/v1/roles/"+roleID+"/permissions", `{"permissions":["Server.View","Console.View","Console.Send","Files.View","Files.Download"]}`, csrf)
	expectStatus(userClient, http.MethodGet, "/api/v1/servers/"+id+"/files/download?path=tmp-e2e-helper.exe", http.StatusOK)
	viewer.Close()
	fmt.Println("E2E_RBAC_MILESTONE5B_OK")
}
