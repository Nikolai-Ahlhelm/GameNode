//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const base = "http://127.0.0.1:18443"

type apiClient struct {
	h    *http.Client
	csrf string
}

type record struct {
	Server struct {
		ID string `json:"id"`
	} `json:"server"`
	Runtime struct {
		PID          int    `json:"pid"`
		CurrentState string `json:"current_state"`
		CrashCount   int    `json:"crash_count"`
		RestartCount int    `json:"restart_count"`
		ExitCode     *int   `json:"exit_code"`
	} `json:"runtime"`
}

type event struct {
	Client, Type, Stream, Data, State string
}

func (c *apiClient) do(method, path string, body any, csrf bool, out any) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		must(err)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, base+path, reader)
	must(err)
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.h.Do(req)
	must(err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	must(err)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		panic(fmt.Sprintf("%s %s: %d %s", method, path, resp.StatusCode, data))
	}
	if out != nil {
		must(json.Unmarshal(data, out))
	}
}

func dial(c *apiClient, serverID, name string, sink chan<- event, wg *sync.WaitGroup) *websocket.Conn {
	u, _ := url.Parse(base)
	headers := http.Header{"Origin": []string{base}}
	for _, cookie := range c.h.Jar.Cookies(u) {
		headers.Add("Cookie", cookie.String())
	}
	wsURL := "ws://127.0.0.1:18443/api/v1/servers/" + serverID + "/console/ws"
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		if response != nil {
			panic(fmt.Sprintf("dial %s: %v (%s)", name, err, response.Status))
		}
		panic(err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			var message struct{ Type, Stream, Data, State string }
			if err := conn.ReadJSON(&message); err != nil {
				return
			}
			sink <- event{name, message.Type, message.Stream, message.Data, message.State}
		}
	}()
	return conn
}

func waitState(c *apiClient, id, wanted string, timeout time.Duration) record {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var current record
		c.do(http.MethodGet, "/api/v1/servers/"+id, nil, false, &current)
		if current.Runtime.CurrentState == wanted {
			return current
		}
		time.Sleep(250 * time.Millisecond)
	}
	panic("timed out waiting for state " + wanted)
}

func waitOutput(events <-chan event, timeout time.Duration, matches ...string) (string, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var output strings.Builder
	for {
		select {
		case message := <-events:
			if message.Type == "output" {
				output.WriteString(message.Data)
				lower := strings.ToLower(message.Data)
				for _, match := range matches {
					if strings.Contains(lower, strings.ToLower(match)) {
						return output.String(), true
					}
				}
			}
		case <-deadline.C:
			return output.String(), false
		}
	}
}

func main() {
	jar, err := cookiejar.New(nil)
	must(err)
	c := &apiClient{h: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
	var setup struct {
		CSRF string `json:"csrf_token"`
	}
	c.do(http.MethodPost, "/api/v1/setup", map[string]any{"username": "acceptance-admin", "email": "acceptance@example.test", "password": "AcceptancePassword123!"}, false, &setup)
	c.csrf = setup.CSRF

	var list struct {
		Templates []struct {
			ID         string `json:"id"`
			SourceType string `json:"source_type"`
			ReadOnly   bool   `json:"read_only"`
		} `json:"templates"`
	}
	c.do(http.MethodGet, "/api/v1/templates", nil, false, &list)
	found := false
	for _, template := range list.Templates {
		if template.ID == "builtin-minecraft-neoforge" && template.SourceType == "builtin" && template.ReadOnly {
			found = true
		}
	}
	if !found {
		panic("built-in NeoForge template missing")
	}

	root := `I:\GameNode\server-test`
	request := map[string]any{"server_name": "NeoForge Real-World Acceptance", "server_root": root, "minimum_memory_mb": 1024, "maximum_memory_mb": 4096, "nogui": true}
	var preview map[string]any
	c.do(http.MethodPost, "/api/v1/templates/builtin-minecraft-neoforge/resolve", request, false, &preview)
	if preview["java_found"] != true {
		panic("resolver did not discover Java")
	}
	fmt.Printf("RESOLVE neoforge=%v minecraft=%v java=%v\n", preview["neoforge_version"], preview["minecraft_version"], preview["executable"])

	var created record
	c.do(http.MethodPost, "/api/v1/templates/builtin-minecraft-neoforge/adopt", request, true, &created)
	id := created.Server.ID
	c.do(http.MethodPost, "/api/v1/servers/"+id+"/start", nil, true, &created)
	first := waitState(c, id, "running", 10*time.Second)
	fmt.Printf("START pid=%d state=%s\n", first.Runtime.PID, first.Runtime.CurrentState)

	events := make(chan event, 4096)
	var wg sync.WaitGroup
	ws1 := dial(c, id, "client-1", events, &wg)
	ws2 := dial(c, id, "client-2", events, &wg)
	output, ready := waitOutput(events, 180*time.Second, "done (", "you need to agree to the eula", "failed to start")
	lower := strings.ToLower(output)
	if strings.Contains(lower, "eula") {
		fmt.Println("EULA_REQUIRED")
		fmt.Print(output)
		ws1.Close()
		ws2.Close()
		os.Exit(2)
	}
	if !ready || !strings.Contains(lower, "done (") {
		fmt.Println("STARTUP_NOT_READY")
		fmt.Print(output)
		ws1.Close()
		ws2.Close()
		os.Exit(3)
	}
	fmt.Println("CONSOLE_READY stdout=true clients=2")
	must(ws1.WriteJSON(map[string]string{"type": "input", "data": "help\n"}))
	helpOutput, help := waitOutput(events, 30*time.Second, "help: ", "commands", "<--[here]")
	if !help {
		fmt.Println("HELP_RESPONSE_MISSING")
		fmt.Print(helpOutput)
		os.Exit(4)
	}
	fmt.Println("HELP_ACCEPTED")
	ws2.Close()
	ws3 := dial(c, id, "client-reconnect", events, &wg)
	history, historyOK := waitOutput(events, 10*time.Second, "done (", "help")
	if !historyOK {
		fmt.Println("RECONNECT_HISTORY_MISSING")
		fmt.Print(history)
		os.Exit(5)
	}
	fmt.Println("RECONNECT_HISTORY_ACCEPTED")

	c.do(http.MethodPost, "/api/v1/servers/"+id+"/stop", nil, true, &created)
	stopped := waitState(c, id, "stopped", 70*time.Second)
	fmt.Printf("STOP state=%s exit=%v crashes=%d\n", stopped.Runtime.CurrentState, stopped.Runtime.ExitCode, stopped.Runtime.CrashCount)
	ws1.Close()
	ws3.Close()

	c.do(http.MethodPost, "/api/v1/servers/"+id+"/start", nil, true, &created)
	second := waitState(c, id, "running", 10*time.Second)
	if second.Runtime.PID == first.Runtime.PID {
		panic("restart reused PID")
	}
	fmt.Printf("RESTART pid=%d previous=%d\n", second.Runtime.PID, first.Runtime.PID)
	ws4 := dial(c, id, "client-new-session", events, &wg)
	secondOutput, secondReady := waitOutput(events, 120*time.Second, "done (")
	if !secondReady {
		fmt.Print(secondOutput)
		panic("second start did not become ready")
	}
	c.do(http.MethodPost, "/api/v1/servers/"+id+"/stop", nil, true, &created)
	final := waitState(c, id, "stopped", 70*time.Second)
	ws4.Close()
	fmt.Printf("FINAL state=%s crashes=%d restarts=%d\n", final.Runtime.CurrentState, final.Runtime.CrashCount, final.Runtime.RestartCount)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
