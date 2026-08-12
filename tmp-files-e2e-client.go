//go:build ignore

// This isolated Windows acceptance client intentionally lives outside the
// normal Go test package. Run it with:
//
//	go run tmp-files-e2e-client.go

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const projectRoot = "I:/GameNode"

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type serverCreateResponse struct {
	Server struct {
		ID string `json:"id"`
	} `json:"server"`
}

type listResponse struct {
	Entries []struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"entries"`
}

type contentResponse struct {
	Content string `json:"content"`
}

type environment struct {
	baseURL string
	client  *http.Client
	csrf    string
	server  string
	root    string
	node    *exec.Cmd
}

func main() {
	if runtime.GOOS != "windows" {
		panic("the real acceptance client must run on Windows")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	env := startEnvironment(ctx)
	defer env.close()

	testFilesystemFlow(ctx, env)
	testTraversalMatrix(ctx, env)
	testUploadHardening(ctx, env)
	testWindowsReparsePoints(ctx, env)

	fmt.Println("E2E_FILESYSTEM_MILESTONE4_OK")
}

func startEnvironment(ctx context.Context) *environment {
	temp, err := os.MkdirTemp("", "gamenode-files-e2e-*")
	must(err)
	root := filepath.Join(temp, "server-root")
	must(os.Mkdir(root, 0o755))
	helper := filepath.Join(projectRoot, "tmp-e2e-helper.exe")
	if _, err := os.Stat(helper); err != nil {
		must(fmt.Errorf("helper executable unavailable: %w", err))
	}
	must(copyFile(filepath.Join(root, "helper.exe"), helper))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	address := listener.Addr().String()
	must(listener.Close())
	baseURL := "http://" + address
	config := filepath.Join(temp, "config.yaml")
	data := filepath.Join(temp, "data")
	configText := fmt.Sprintf("server:\n  listen: %q\ndata:\n  directory: %q\ndatabase:\n  path: %q\nfilesystem:\n  max_upload_bytes: 1048576\nlogging:\n  level: error\n", address, filepath.ToSlash(data), filepath.ToSlash(filepath.Join(data, "gamenode.db")))
	must(os.WriteFile(config, []byte(configText), 0o600))

	node := exec.Command(filepath.Join(projectRoot, "dist", "gamenode-windows-amd64.exe"), "-config", config)
	node.Stdout = io.Discard
	node.Stderr = io.Discard
	must(node.Start())
	env := &environment{baseURL: baseURL, root: root, node: node}
	ready := false
	defer func() {
		if !ready {
			env.close()
		}
	}()

	jar, err := cookiejar.New(nil)
	must(err)
	env.client = &http.Client{Jar: jar, Timeout: 10 * time.Second}
	waitForAPI(ctx, env.client, baseURL)

	status, body := env.json(ctx, http.MethodPost, "/api/v1/setup", map[string]string{
		"username": "filese2e",
		"email":    "files-e2e@example.test",
		"password": "a password long enough",
	}, "")
	mustStatus("initial setup", status, body, http.StatusOK)
	status, body = env.json(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "filese2e",
		"password": "a password long enough",
	}, "")
	mustStatus("login", status, body, http.StatusOK)
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	must(json.Unmarshal(body, &login))
	if login.CSRF == "" {
		panic("login did not return CSRF token")
	}
	env.csrf = login.CSRF

	status, body = env.json(ctx, http.MethodPost, "/api/v1/servers", map[string]any{
		"creation_mode":         "custom",
		"name":                  "files-e2e",
		"working_directory":     root,
		"executable":            "helper.exe",
		"arguments":             []string{},
		"environment_variables": map[string]string{},
		"stop_timeout_seconds":  2,
	}, env.csrf)
	mustStatus("create server", status, body, http.StatusCreated)
	var created serverCreateResponse
	must(json.Unmarshal(body, &created))
	if created.Server.ID == "" {
		panic("server creation returned no ID")
	}
	env.server = created.Server.ID
	ready = true
	return env
}

func (e *environment) close() {
	if e.node != nil && e.node.Process != nil {
		_ = e.node.Process.Kill()
		_, _ = e.node.Process.Wait()
	}
	if e.root != "" {
		_ = os.RemoveAll(filepath.Dir(e.root))
	}
}

func (e *environment) json(ctx context.Context, method, path string, value any, csrf string) (int, []byte) {
	var body io.Reader
	if value != nil {
		encoded, err := json.Marshal(value)
		must(err)
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, body)
	must(err)
	request.Header.Set("Origin", e.baseURL)
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := e.client.Do(request)
	must(err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	must(err)
	return response.StatusCode, data
}

func (e *environment) request(ctx context.Context, method, path string, csrf string) (int, http.Header, []byte) {
	request, err := http.NewRequestWithContext(ctx, method, e.baseURL+path, nil)
	must(err)
	request.Header.Set("Origin", e.baseURL)
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := e.client.Do(request)
	must(err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	must(err)
	return response.StatusCode, response.Header, data
}

func (e *environment) upload(ctx context.Context, directory, filename string, data []byte, overwrite bool) (int, []byte) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	must(err)
	_, err = part.Write(data)
	must(err)
	must(writer.Close())
	query := url.Values{"path": {directory}}
	if overwrite {
		query.Set("overwrite", "true")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/v1/servers/"+e.server+"/files/upload?"+query.Encode(), &body)
	must(err)
	request.Header.Set("Origin", e.baseURL)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", e.csrf)
	response, err := e.client.Do(request)
	must(err)
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	must(err)
	return response.StatusCode, result
}

func testFilesystemFlow(ctx context.Context, e *environment) {
	base := "/api/v1/servers/" + e.server + "/files"
	status, _, body := e.request(ctx, http.MethodGet, base, "")
	mustStatus("list root", status, body, http.StatusOK)

	status, body = e.json(ctx, http.MethodPost, base+"/directory", map[string]string{"path": "config"}, e.csrf)
	mustStatus("create directory", status, body, http.StatusCreated)
	status, body = e.json(ctx, http.MethodPost, base+"/file", map[string]string{"path": "config/server.properties", "content": "motd=before"}, e.csrf)
	mustStatus("create file", status, body, http.StatusCreated)
	status, body = e.json(ctx, http.MethodPut, base+"/content", map[string]string{"path": "config/server.properties", "content": "motd=after"}, e.csrf)
	mustStatus("edit file", status, body, http.StatusNoContent)
	status, _, body = e.request(ctx, http.MethodGet, base+"/content?"+url.Values{"path": {"config/server.properties"}}.Encode(), "")
	mustStatus("read edited file", status, body, http.StatusOK)
	var content contentResponse
	must(json.Unmarshal(body, &content))
	if content.Content != "motd=after" {
		panic("read back content mismatch")
	}
	status, body = e.json(ctx, http.MethodPost, base+"/move", map[string]string{"source": "config/server.properties", "destination": "config/renamed.properties"}, e.csrf)
	mustStatus("rename file", status, body, http.StatusNoContent)
	status, body = e.json(ctx, http.MethodPost, base+"/directory", map[string]string{"path": "archive"}, e.csrf)
	mustStatus("create move directory", status, body, http.StatusCreated)
	status, body = e.json(ctx, http.MethodPost, base+"/move", map[string]string{"source": "config/renamed.properties", "destination": "archive/server.properties"}, e.csrf)
	mustStatus("move file", status, body, http.StatusNoContent)
	status, _, body = e.request(ctx, http.MethodGet, base+"?"+url.Values{"path": {"archive"}}.Encode(), "")
	mustStatus("list moved file", status, body, http.StatusOK)
	var listed listResponse
	must(json.Unmarshal(body, &listed))
	if len(listed.Entries) != 1 || listed.Entries[0].Path != "archive/server.properties" {
		panic(fmt.Sprintf("moved file missing from listing: %#v", listed.Entries))
	}
}

func testTraversalMatrix(ctx context.Context, e *environment) {
	base := "/api/v1/servers/" + e.server + "/files"
	for _, value := range []string{
		"../outside",
		"..\\outside",
		"nested/../../outside",
		"nested\\..\\..\\outside",
		"/etc/passwd",
		"C:\\Windows\\win.ini",
		"\\\\server\\share\\file.txt",
		"../" + filepath.Base(e.root) + "-evil/secret.txt",
	} {
		status, _, body := e.request(ctx, http.MethodGet, base+"?"+url.Values{"path": {value}}.Encode(), "")
		mustStatus("traversal "+value, status, body, http.StatusBadRequest)
	}
	status, _, body := e.request(ctx, http.MethodGet, base+"?path=%2e%2e%2foutside", "")
	mustStatus("URL-encoded traversal", status, body, http.StatusBadRequest)

	status, body = e.json(ctx, http.MethodPost, base+"/move", map[string]string{"source": "archive/server.properties", "destination": "../outside"}, e.csrf)
	mustStatus("move destination escape", status, body, http.StatusBadRequest)
	status, body = e.json(ctx, http.MethodPost, base+"/move", map[string]string{"source": "../outside", "destination": "archive/nope"}, e.csrf)
	mustStatus("move source escape", status, body, http.StatusBadRequest)
	status, _, body = e.request(ctx, http.MethodDelete, base+"?path=.&recursive=true", e.csrf)
	mustStatus("root deletion", status, body, http.StatusBadRequest)
}

func testUploadHardening(ctx context.Context, e *environment) {
	base := "/api/v1/servers/" + e.server + "/files"
	payload := bytes.Repeat([]byte{0, 1, 2, 3, 4, 5, 6, 7}, 64<<10)
	status, body := e.upload(ctx, "archive", "payload.bin", payload, false)
	mustStatus("upload binary", status, body, http.StatusCreated)
	status, body = e.upload(ctx, "archive", "payload.bin", []byte("conflict"), false)
	mustStatus("upload conflict", status, body, http.StatusConflict)
	status, body = e.upload(ctx, "archive", "payload.bin", []byte("replacement"), true)
	mustStatus("upload overwrite", status, body, http.StatusCreated)
	status, body = e.upload(ctx, "archive", "payload.bin", payload, true)
	mustStatus("upload binary replacement", status, body, http.StatusCreated)

	for _, filename := range []string{"../bad-traversal.bin", "dir/bad-forward.bin", "dir\\bad-backward.bin", "C:\\bad-drive.bin", "\\\\host\\bad-unc.bin"} {
		status, body = e.upload(ctx, "archive", filename, payload, false)
		if status != http.StatusCreated && status != http.StatusBadRequest {
			mustStatus("malicious filename "+filename, status, body, http.StatusBadRequest)
		}
		if status == http.StatusCreated {
			// net/http's multipart parser strips a submitted directory component
			// before FileName reaches the API. Verify that the resulting file is
			// still only created below the selected server-relative directory.
			name := filepath.Base(filename)
			if _, err := os.Stat(filepath.Join(e.root, "archive", name)); err != nil {
				panic(fmt.Sprintf("sanitized upload %q did not remain in target directory: %v", filename, err))
			}
		}
	}
	status, body = e.upload(ctx, "../outside", "safe.bin", payload, false)
	mustStatus("upload target escape", status, body, http.StatusBadRequest)
	status, body = e.upload(ctx, "archive", "too-large.bin", make([]byte, 1048577), false)
	mustStatus("oversized upload", status, body, http.StatusRequestEntityTooLarge)
	if matches, err := filepath.Glob(filepath.Join(e.root, "archive", ".gamenode-upload-*")); err != nil || len(matches) != 0 {
		panic(fmt.Sprintf("orphan upload temporary files: %v %v", matches, err))
	}

	status, headers, downloaded := e.request(ctx, http.MethodGet, base+"/download?"+url.Values{"path": {"archive/payload.bin"}}.Encode(), "")
	mustStatus("download binary", status, downloaded, http.StatusOK)
	if !bytes.Equal(downloaded, payload) || hash(downloaded) != hash(payload) {
		panic("download byte integrity mismatch")
	}
	if !strings.Contains(headers.Get("Content-Disposition"), "payload.bin") || strings.Contains(headers.Get("Content-Disposition"), e.root) || headers.Get("X-Content-Type-Options") != "nosniff" {
		panic(fmt.Sprintf("unsafe download headers: %v", headers))
	}
	status, _, body = e.request(ctx, http.MethodGet, base+"/download?"+url.Values{"path": {"archive"}}.Encode(), "")
	mustStatus("directory download", status, body, http.StatusBadRequest)
	status, _, body = e.request(ctx, http.MethodGet, base+"/download?path=%2e%2e%2foutside.bin", "")
	mustStatus("outside download", status, body, http.StatusBadRequest)

	status, body = e.json(ctx, http.MethodPost, base+"/directory", map[string]string{"path": "delete-tree"}, e.csrf)
	mustStatus("create delete tree", status, body, http.StatusCreated)
	status, body = e.json(ctx, http.MethodPost, base+"/file", map[string]string{"path": "delete-tree/item.txt", "content": "delete"}, e.csrf)
	mustStatus("create delete-tree file", status, body, http.StatusCreated)
	status, _, body = e.request(ctx, http.MethodDelete, base+"?"+url.Values{"path": {"delete-tree"}}.Encode(), e.csrf)
	mustStatus("non-recursive delete", status, body, http.StatusConflict)
	status, _, body = e.request(ctx, http.MethodDelete, base+"?"+url.Values{"path": {"delete-tree"}, "recursive": {"true"}}.Encode(), e.csrf)
	mustStatus("explicit recursive delete", status, body, http.StatusNoContent)
	status, _, body = e.request(ctx, http.MethodGet, base+"?"+url.Values{"path": {"delete-tree"}}.Encode(), "")
	mustStatus("deleted tree", status, body, http.StatusNotFound)
}

func testWindowsReparsePoints(ctx context.Context, e *environment) {
	outside := filepath.Join(filepath.Dir(e.root), "outside")
	must(os.MkdirAll(outside, 0o755))
	must(os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
	base := "/api/v1/servers/" + e.server + "/files"
	link := filepath.Join(e.root, "outside-link")
	if err := os.Symlink(outside, link); err == nil {
		status, _, body := e.request(ctx, http.MethodGet, base+"?"+url.Values{"path": {"outside-link"}}.Encode(), "")
		mustStatus("Windows symlink escape", status, body, http.StatusBadRequest)
		must(os.Remove(link))
		fmt.Println("E2E_WINDOWS_SYMLINK_OK")
	} else {
		fmt.Println("E2E_WINDOWS_SYMLINK_SKIPPED: " + err.Error())
	}

	junction := filepath.Join(e.root, "outside-junction")
	command := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, outside)
	if output, err := command.CombinedOutput(); err == nil {
		status, _, body := e.request(ctx, http.MethodGet, base+"?"+url.Values{"path": {"outside-junction"}}.Encode(), "")
		mustStatus("Windows junction escape", status, body, http.StatusBadRequest)
		must(os.Remove(junction))
		fmt.Println("E2E_WINDOWS_JUNCTION_OK")
	} else {
		fmt.Println("E2E_WINDOWS_JUNCTION_SKIPPED: " + strings.TrimSpace(string(output)))
	}
}

func waitForAPI(ctx context.Context, client *http.Client, baseURL string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/setup/status", nil)
		must(err)
		response, err := client.Do(request)
		if err == nil && response.StatusCode == http.StatusOK {
			response.Body.Close()
			return
		}
		if response != nil {
			response.Body.Close()
		}
		select {
		case <-ctx.Done():
			panic("GameNode API readiness timeout")
		case <-ticker.C:
		}
	}
}

func copyFile(destination, source string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustStatus(step string, actual int, body []byte, expected int) {
	if actual != expected {
		var apiErr apiError
		_ = json.Unmarshal(body, &apiErr)
		panic(fmt.Sprintf("%s: status=%d expected=%d code=%s message=%s", step, actual, expected, apiErr.Error.Code, apiErr.Error.Message))
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
