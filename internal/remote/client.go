// Package remote implements the narrow, typed Remote Node client boundary
// the controller side of GameNode uses to talk to another GameNode instance
// over its authenticated Node API. There is no generic
// DoRequest(method, arbitraryURL, arbitraryBody) escape hatch: every
// operation is a named method against exactly the one enrolled endpoint
// passed to it, with a bounded timeout, a bounded response body, and no
// cross-host redirect following (see AGENTS.md and
// docs/adr/0006-remote-node-foundation.md's SSRF section).
package remote

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"gamenode/internal/provisioning"
	"github.com/gorilla/websocket"
)

// DefaultTimeout bounds every single request this client makes. Callers may
// still supply a shorter context deadline.
const DefaultTimeout = 8 * time.Second

// MaxResponseBytes bounds how much of a Node API response body this client
// will read, regardless of what the remote end claims in Content-Length.
const MaxResponseBytes = 1 << 20 // 1 MiB

// MaxFileBytes is the explicit binary transfer ceiling. It matches the
// local filesystem upload limit and prevents a remote node from turning the
// controller into an unbounded byte relay.
const MaxFileBytes int64 = 64 << 20

// Kind classifies a remote-node error into the small set of product-level
// concepts the UI and audit layer are allowed to see. Raw transport/TLS
// errors never reach those layers (see AGENTS.md item 28).
type Kind string

const (
	KindUnreachable          Kind = "node_unreachable"
	KindAuthenticationFailed Kind = "node_authentication_failed"
	KindProtocolIncompatible Kind = "node_protocol_incompatible"
	KindMalformedResponse    Kind = "node_malformed_response"
	KindOversizedResponse    Kind = "node_response_too_large"
	// KindResourceNotFound/KindResourceConflict cover the two remote-server
	// outcomes common enough (server deleted concurrently on the node;
	// lifecycle action rejected because of the server's current state) to
	// deserve their own controlled code, distinct from the generic
	// KindMalformedResponse bucket every other 4xx status falls into. Note
	// this is deliberately NOT a passthrough of the remote node's own error
	// body/message - only its HTTP status is trusted (see AGENTS.md item 28).
	KindResourceNotFound Kind = "node_resource_not_found"
	KindResourceConflict Kind = "node_resource_conflict"
)

// Error is the sanitized error this package ever returns. The Detail field
// is safe to log; it deliberately never embeds a raw Go network/TLS error
// value that might contain a local file path.
type Error struct {
	Kind   Kind
	Detail string
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Detail }

// ValidateEndpoint normalizes and validates an operator-supplied Remote Node
// endpoint. Only scheme://host[:port] is retained - no path, query,
// fragment, or userinfo survives, since the client only ever calls its own
// fixed, typed paths against this base (see AGENTS.md item 16/31).
func ValidateEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("endpoint is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("endpoint is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("endpoint must use http or https")
	}
	if u.User != nil {
		return "", errors.New("endpoint must not contain userinfo")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("endpoint must not contain a path")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("endpoint must not contain a query or fragment")
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("endpoint must specify a host")
	}
	if u.Port() != "" {
		if _, err := net.LookupPort("tcp", u.Port()); err != nil {
			return "", errors.New("endpoint port is invalid")
		}
	}
	return u.Scheme + "://" + u.Host, nil
}

// Client is the only way higher product layers reach a Remote Node. Every
// method below builds its own fixed path against the validated endpoint; no
// caller may substitute an arbitrary URL or body.
type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{
		Timeout: DefaultTimeout,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12}, // certificate verification stays on; see docs/adr/0006 - no InsecureSkipVerify.
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
		},
		// A Remote Node redirecting the client to a different origin would be
		// an SSRF/credential-leak primitive (the Authorization header would
		// otherwise follow it via Go's default client). Stop at the first
		// response instead of ever re-issuing the request elsewhere.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

type EnrollResult struct {
	NodeID          string   `json:"node_id"`
	DisplayName     string   `json:"display_name"`
	Credential      string   `json:"credential"`
	ProtocolVersion int      `json:"protocol_version"`
	GameNodeVersion string   `json:"gamenode_version"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Capabilities    []string `json:"capabilities"`
}

// Enroll exchanges a one-time pairing token for a durable machine
// credential. It intentionally sends no prior Authorization header - trust
// is established by the pairing token alone, and the credential in the
// response must be persisted by the caller immediately: this package never
// stores or resends it.
func (c *Client) Enroll(ctx context.Context, endpoint, pairingToken string) (EnrollResult, error) {
	body, err := json.Marshal(map[string]string{"pairing_token": pairingToken})
	if err != nil {
		return EnrollResult{}, &Error{Kind: KindMalformedResponse, Detail: "encode enrollment request"}
	}
	var result EnrollResult
	err = c.do(ctx, http.MethodPost, endpoint, "/api/v1/node/enroll", "", bytes.NewReader(body), &result)
	return result, err
}

type NodeInfo struct {
	NodeID          string   `json:"node_id"`
	DisplayName     string   `json:"display_name"`
	GameNodeVersion string   `json:"gamenode_version"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	ProtocolVersion int      `json:"protocol_version"`
	Capabilities    []string `json:"capabilities"`
	UptimeSeconds   int64    `json:"uptime_seconds"`
}

func (c *Client) GetNodeInfo(ctx context.Context, endpoint, credential string) (NodeInfo, error) {
	var result NodeInfo
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/info", credential, nil, &result)
	return result, err
}

type HealthResult struct {
	Status string `json:"status"`
}

func (c *Client) GetHealth(ctx context.Context, endpoint, credential string) (HealthResult, error) {
	var result HealthResult
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/health", credential, nil, &result)
	return result, err
}

type CapabilitiesResult struct {
	Capabilities []string `json:"capabilities"`
}

func (c *Client) GetCapabilities(ctx context.Context, endpoint, credential string) (CapabilitiesResult, error) {
	var result CapabilitiesResult
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/capabilities", credential, nil, &result)
	return result, err
}

// ProvisioningRequest is the bounded transport contract for invoking the
// target node's existing provisioning.Service. It deliberately contains no
// Docker flags, mounts, devices, credentials, scripts, or arbitrary payload.
type ProvisioningRequest struct {
	TemplateID       string            `json:"template_id"`
	ServerName       string            `json:"server_name"`
	DirectoryName    string            `json:"directory_name"`
	Variables        map[string]string `json:"variables"`
	RecoverExisting  bool              `json:"recover_existing"`
	TenantID         string            `json:"tenant_id"`
	RuntimeType      string            `json:"runtime_type"`
	Image            string            `json:"image"`
	MemoryLimitBytes int64             `json:"memory_limit_bytes"`
	CPULimitMillis   int               `json:"cpu_limit_millis"`
	PIDsLimit        int64             `json:"pids_limit"`
	TmpfsSizeBytes   int64             `json:"tmpfs_size_bytes"`
}

func (c *Client) StartProvisioning(ctx context.Context, endpoint, credential string, req ProvisioningRequest) (provisioning.Job, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return provisioning.Job{}, &Error{Kind: KindMalformedResponse, Detail: "encode provisioning request"}
	}
	var job provisioning.Job
	err = c.doProvisioning(ctx, http.MethodPost, endpoint, "/api/v1/node/provisioning", credential, bytes.NewReader(body), &job)
	return job, err
}

func (c *Client) GetProvisioningJob(ctx context.Context, endpoint, credential, jobID string) (provisioning.Job, error) {
	var job provisioning.Job
	err := c.doProvisioning(ctx, http.MethodGet, endpoint, "/api/v1/node/provisioning/"+url.PathEscape(jobID), credential, nil, &job)
	return job, err
}

func (c *Client) CancelProvisioningJob(ctx context.Context, endpoint, credential, jobID string) (provisioning.Job, error) {
	var job provisioning.Job
	err := c.doProvisioning(ctx, http.MethodPost, endpoint, "/api/v1/node/provisioning/"+url.PathEscape(jobID)+"/cancel", credential, nil, &job)
	return job, err
}

type ProvisioningError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ProvisioningError) Error() string { return e.Code + ": " + e.Message }

type provisioningErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) doProvisioning(ctx context.Context, method, endpoint, requestPath, credential string, body io.Reader, out any) error {
	status, data, err := c.roundTrip(ctx, method, endpoint, requestPath, credential, body)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Kind: KindAuthenticationFailed, Detail: fmt.Sprintf("status %d", status)}
	case status == http.StatusUpgradeRequired || status == http.StatusPreconditionFailed:
		return &Error{Kind: KindProtocolIncompatible, Detail: fmt.Sprintf("status %d", status)}
	case status >= 300 && status < 400:
		return &Error{Kind: KindMalformedResponse, Detail: fmt.Sprintf("unexpected redirect status %d", status)}
	case status >= 400:
		var response provisioningErrorBody
		if json.Unmarshal(data, &response) == nil && response.Error.Code != "" {
			return &ProvisioningError{StatusCode: status, Code: response.Error.Code, Message: response.Error.Message}
		}
		return &Error{Kind: KindMalformedResponse, Detail: fmt.Sprintf("status %d", status)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Kind: KindMalformedResponse, Detail: "invalid response body"}
	}
	return nil
}

// --- Remote Server Management (v0.5B) / Operational Hardening (v0.5C) ---
//
// Every method below is a fixed, typed call against exactly one enrolled
// Node API path - see this package's doc comment. None of them accept an
// arbitrary path or method from a caller; server IDs and file paths are
// interpolated only as URL path/query components, never as a base URL or
// host.

// RuntimeState mirrors the bounded runtime status fields a Node API
// response carries for one of its local servers.
type RuntimeState struct {
	PID             int        `json:"pid,omitempty"`
	ProcessStartAt  *time.Time `json:"process_started_at,omitempty"`
	LastStartAt     *time.Time `json:"last_start_at,omitempty"`
	LastStopAt      *time.Time `json:"last_stop_at,omitempty"`
	LastExitAt      *time.Time `json:"last_exit_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	LastCrashAt     *time.Time `json:"last_crash_at,omitempty"`
	CrashCount      int        `json:"crash_count"`
	RestartCount    int        `json:"restart_count"`
	LastError       string     `json:"last_error,omitempty"`
	CurrentState    string     `json:"current_state"`
	ConsoleDetached bool       `json:"console_detached,omitempty"`
}

// ServerSummary is the bounded, typed projection of one remote server this
// client ever receives. It deliberately has no working directory, no
// executable path, no arguments, and no environment variables - those never
// leave the remote node (see AGENTS.md item 8).
type ServerSummary struct {
	ID            string       `json:"id"`
	TenantID      string       `json:"tenant_id"`
	Name          string       `json:"name"`
	Description   string       `json:"description,omitempty"`
	CreationMode  string       `json:"creation_mode"`
	RuntimeType   string       `json:"runtime_type"`
	AutoStart     bool         `json:"auto_start"`
	RestartPolicy string       `json:"restart_policy"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	Runtime       RuntimeState `json:"runtime"`
}

// CreateServerInput is the full server definition a controller may forward
// to a remote node's own servers.Service.Create - identical in shape to the
// existing local Create Server contract; the remote node applies exactly
// the same validation and creation-mode rules it applies to a local
// request. It is never persisted or interpreted by the controller itself.
type CreateServerInput struct {
	TenantID             string            `json:"tenant_id,omitempty"`
	Name                 string            `json:"name"`
	Description          string            `json:"description,omitempty"`
	WorkingDirectory     string            `json:"working_directory"`
	Executable           string            `json:"executable"`
	Arguments            []string          `json:"arguments,omitempty"`
	EnvironmentVariables map[string]string `json:"environment_variables,omitempty"`
	RuntimeType          string            `json:"runtime_type,omitempty"`
	AutoStart            bool              `json:"auto_start,omitempty"`
	RestartPolicy        string            `json:"restart_policy,omitempty"`
	StopMethod           string            `json:"stop_method,omitempty"`
	StopCommand          string            `json:"stop_command,omitempty"`
	StopTimeoutSeconds   int               `json:"stop_timeout_seconds,omitempty"`
}

// UpdateServerInput intentionally contains only fields that can be edited
// without exposing the remote node's working directory, executable,
// arguments, environment, or stop command to the controller.
type UpdateServerInput struct {
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	AutoStart     *bool   `json:"auto_start,omitempty"`
	RestartPolicy *string `json:"restart_policy,omitempty"`
}

func (c *Client) ListServers(ctx context.Context, endpoint, credential string) ([]ServerSummary, error) {
	var result struct {
		Servers []ServerSummary `json:"servers"`
	}
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/servers", credential, nil, &result)
	return result.Servers, err
}

func (c *Client) GetServer(ctx context.Context, endpoint, credential, serverID string) (ServerSummary, error) {
	var result struct {
		Server ServerSummary `json:"server"`
	}
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID), credential, nil, &result)
	return result.Server, err
}

func (c *Client) CreateServer(ctx context.Context, endpoint, credential string, in CreateServerInput) (ServerSummary, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return ServerSummary{}, &Error{Kind: KindMalformedResponse, Detail: "encode create request"}
	}
	var result struct {
		Server ServerSummary `json:"server"`
	}
	err = c.do(ctx, http.MethodPost, endpoint, "/api/v1/node/servers", credential, bytes.NewReader(body), &result)
	return result.Server, err
}

func (c *Client) UpdateServer(ctx context.Context, endpoint, credential, serverID string, in UpdateServerInput) (ServerSummary, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return ServerSummary{}, &Error{Kind: KindMalformedResponse, Detail: "encode update request"}
	}
	var result struct {
		Server ServerSummary `json:"server"`
	}
	err = c.do(ctx, http.MethodPatch, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID), credential, bytes.NewReader(body), &result)
	return result.Server, err
}

func (c *Client) DeleteServer(ctx context.Context, endpoint, credential, serverID string) error {
	return c.do(ctx, http.MethodDelete, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID), credential, nil, nil)
}

func (c *Client) serverLifecycle(ctx context.Context, endpoint, credential, serverID, action string) (ServerSummary, error) {
	var result struct {
		Server ServerSummary `json:"server"`
	}
	err := c.do(ctx, http.MethodPost, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/"+action, credential, nil, &result)
	return result.Server, err
}

func (c *Client) StartServer(ctx context.Context, endpoint, credential, serverID string) (ServerSummary, error) {
	return c.serverLifecycle(ctx, endpoint, credential, serverID, "start")
}
func (c *Client) StopServer(ctx context.Context, endpoint, credential, serverID string) (ServerSummary, error) {
	return c.serverLifecycle(ctx, endpoint, credential, serverID, "stop")
}
func (c *Client) RestartServer(ctx context.Context, endpoint, credential, serverID string) (ServerSummary, error) {
	return c.serverLifecycle(ctx, endpoint, credential, serverID, "restart")
}
func (c *Client) KillServer(ctx context.Context, endpoint, credential, serverID string) (ServerSummary, error) {
	return c.serverLifecycle(ctx, endpoint, credential, serverID, "kill")
}

// ConsoleEvent mirrors one bounded, typed console output event.
type ConsoleEvent struct {
	Type      string    `json:"type"`
	Stream    string    `json:"stream,omitempty"`
	Data      string    `json:"data,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ConsoleSnapshot is the bounded poll result for a remote server's console.
type ConsoleSnapshot struct {
	State  string         `json:"state"`
	Events []ConsoleEvent `json:"events"`
}

func (c *Client) GetConsoleSnapshot(ctx context.Context, endpoint, credential, serverID string) (ConsoleSnapshot, error) {
	var result ConsoleSnapshot
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/console", credential, nil, &result)
	return result, err
}

func (c *Client) SendConsoleInput(ctx context.Context, endpoint, credential, serverID, data string) error {
	body, err := json.Marshal(map[string]string{"data": data})
	if err != nil {
		return &Error{Kind: KindMalformedResponse, Detail: "encode console input"}
	}
	return c.do(ctx, http.MethodPost, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/console", credential, bytes.NewReader(body), nil)
}

// MonitoringSnapshot mirrors internal/monitoring.Snapshot's bounded fields.
type MonitoringSnapshot struct {
	Status        string  `json:"status"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryBytes   int64   `json:"memory_bytes"`
	MemoryPercent float64 `json:"memory_percent,omitempty"`
	UptimeSeconds int64   `json:"uptime_seconds,omitempty"`
}

func (c *Client) GetMonitoringSnapshot(ctx context.Context, endpoint, credential, serverID string) (MonitoringSnapshot, error) {
	var result MonitoringSnapshot
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/monitoring", credential, nil, &result)
	return result, err
}

// FileEntry/FileContent mirror internal/filesystem's bounded, typed
// projections for the remote files surface.
type FileEntry struct {
	Name         string    `json:"name"`
	RelativePath string    `json:"path"`
	Type         string    `json:"type"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modified_at"`
	IsSymlink    bool      `json:"is_symlink,omitempty"`
}

type FileContent struct {
	RelativePath string    `json:"path"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modified_at"`
	Encoding     string    `json:"encoding"`
	Content      string    `json:"content"`
}

type FileInfo struct {
	RelativePath string    `json:"path"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modified_at"`
}

type FileDownload struct {
	Body io.ReadCloser
	Size int64
}

type boundedReadCloser struct {
	io.Reader
	closer io.Closer
	limit  int64
	read   int64
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	c.cancel()
	return c.ReadCloser.Close()
}

func (b *boundedReadCloser) Read(p []byte) (int, error) {
	if b.read > b.limit {
		return 0, &Error{Kind: KindOversizedResponse, Detail: "file exceeded size limit"}
	}
	n, err := b.Reader.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, &Error{Kind: KindOversizedResponse, Detail: "file exceeded size limit"}
	}
	return n, err
}
func (b *boundedReadCloser) Close() error { return b.closer.Close() }

func (c *Client) ListFiles(ctx context.Context, endpoint, credential, serverID, path string) ([]FileEntry, error) {
	var result struct {
		Entries []FileEntry `json:"entries"`
	}
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files?path="+url.QueryEscape(path), credential, nil, &result)
	return result.Entries, err
}

func (c *Client) ReadFile(ctx context.Context, endpoint, credential, serverID, path string) (FileContent, error) {
	var result FileContent
	err := c.do(ctx, http.MethodGet, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files/content?path="+url.QueryEscape(path), credential, nil, &result)
	return result, err
}

func (c *Client) WriteFile(ctx context.Context, endpoint, credential, serverID, path, content string) error {
	body, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		return &Error{Kind: KindMalformedResponse, Detail: "encode write file request"}
	}
	return c.do(ctx, http.MethodPut, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files/content", credential, bytes.NewReader(body), nil)
}

// UploadFile streams one multipart file to the fixed Node filesystem route.
// The caller supplies only the already-authorized server-relative directory
// and a browser filename; the target node remains responsible for sandbox
// validation and atomic commit.
func (c *Client) UploadFile(ctx context.Context, endpoint, credential, serverID, targetPath, filename string, data io.Reader, overwrite bool) (FileInfo, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	errCh := make(chan error, 1)
	go func() {
		part, err := mw.CreateFormFile("file", path.Base(filename))
		if err == nil {
			_, err = io.Copy(part, io.LimitReader(data, MaxFileBytes+1))
		}
		if err == nil {
			err = mw.Close()
		} else {
			_ = mw.Close()
		}
		_ = pw.CloseWithError(err)
		errCh <- err
	}()
	q := "?path=" + url.QueryEscape(targetPath) + "&overwrite=" + url.QueryEscape(strconv.FormatBool(overwrite))
	var result FileInfo
	err := c.doContent(ctx, http.MethodPost, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files/upload"+q, credential, pr, mw.FormDataContentType(), &result)
	if copyErr := <-errCh; err == nil && copyErr != nil {
		err = &Error{Kind: KindUnreachable, Detail: "upload stream failed"}
	}
	return result, err
}

// mwPartWriter exists only to make the upload copy site explicit and keep
// the multipart writer hidden from callers.
type mwPartWriter struct{ io.Writer }

func (c *Client) DownloadFile(ctx context.Context, endpoint, credential, serverID, filePath string) (FileDownload, error) {
	resp, err := c.doRaw(ctx, http.MethodGet, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files/download?path="+url.QueryEscape(filePath), credential, nil, "")
	if err != nil {
		return FileDownload{}, err
	}
	if resp.ContentLength > MaxFileBytes {
		_ = resp.Body.Close()
		return FileDownload{}, &Error{Kind: KindOversizedResponse, Detail: "file exceeded size limit"}
	}
	return FileDownload{Body: &boundedReadCloser{Reader: io.LimitReader(resp.Body, MaxFileBytes+1), closer: resp.Body, limit: MaxFileBytes}, Size: resp.ContentLength}, nil
}

func (c *Client) OpenConsoleRelay(ctx context.Context, endpoint, credential, serverID string) (*websocket.Conn, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, &Error{Kind: KindUnreachable, Detail: "invalid node endpoint"}
	}
	if u.Scheme == "http" {
		u.Scheme = "ws"
	} else {
		u.Scheme = "wss"
	}
	u.Path = "/api/v1/node/servers/" + url.PathEscape(serverID) + "/console/ws"
	u.RawQuery = ""
	u.Fragment = ""
	dialer := websocket.Dialer{HandshakeTimeout: DefaultTimeout, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	conn, resp, err := dialer.DialContext(ctx, u.String(), http.Header{"Authorization": []string{"Bearer " + credential}})
	if err != nil {
		if resp != nil {
			return nil, classifyHTTPStatus(resp.StatusCode)
		}
		return nil, &Error{Kind: KindUnreachable, Detail: classifyTransportError(err)}
	}
	return conn, nil
}

func (c *Client) CreateFile(ctx context.Context, endpoint, credential, serverID, path, content string) error {
	body, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		return &Error{Kind: KindMalformedResponse, Detail: "encode create file request"}
	}
	return c.do(ctx, http.MethodPost, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files/file", credential, bytes.NewReader(body), nil)
}

func (c *Client) CreateDirectory(ctx context.Context, endpoint, credential, serverID, path string) error {
	body, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return &Error{Kind: KindMalformedResponse, Detail: "encode create directory request"}
	}
	return c.do(ctx, http.MethodPost, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files/directory", credential, bytes.NewReader(body), nil)
}

func (c *Client) MoveFile(ctx context.Context, endpoint, credential, serverID, source, destination string) error {
	body, err := json.Marshal(map[string]string{"source": source, "destination": destination})
	if err != nil {
		return &Error{Kind: KindMalformedResponse, Detail: "encode move request"}
	}
	return c.do(ctx, http.MethodPost, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files/move", credential, bytes.NewReader(body), nil)
}

func (c *Client) DeleteFile(ctx context.Context, endpoint, credential, serverID, path string, recursive bool) error {
	q := "?path=" + url.QueryEscape(path)
	if recursive {
		q += "&recursive=true"
	}
	return c.do(ctx, http.MethodDelete, endpoint, "/api/v1/node/servers/"+url.PathEscape(serverID)+"/files"+q, credential, nil, nil)
}

func (c *Client) do(ctx context.Context, method, endpoint, path, credential string, body io.Reader, out any) error {
	return c.doContent(ctx, method, endpoint, path, credential, body, "application/json", out)
}

func (c *Client) doContent(ctx context.Context, method, endpoint, path, credential string, body io.Reader, contentType string, out any) error {
	resp, err := c.doRaw(ctx, method, endpoint, path, credential, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return &Error{Kind: KindUnreachable, Detail: "read response"}
	}
	if len(data) > MaxResponseBytes {
		return &Error{Kind: KindOversizedResponse, Detail: "response exceeded size limit"}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Kind: KindMalformedResponse, Detail: "invalid response body"}
	}
	return nil
}

func (c *Client) doRaw(ctx context.Context, method, endpoint, requestPath, credential string, body io.Reader, contentType string) (*http.Response, error) {
	reqCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		reqCtx, cancel = context.WithTimeout(ctx, DefaultTimeout)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, endpoint+requestPath, body)
	if err != nil {
		cancel()
		return nil, &Error{Kind: KindUnreachable, Detail: "build request"}
	}
	if body != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, &Error{Kind: KindUnreachable, Detail: classifyTransportError(err)}
	}
	if err := classifyHTTPStatus(resp.StatusCode); err != nil {
		cancel()
		_ = resp.Body.Close()
		return nil, err
	}
	resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func classifyHTTPStatus(status int) *Error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Kind: KindAuthenticationFailed, Detail: fmt.Sprintf("status %d", status)}
	case status == http.StatusUpgradeRequired || status == http.StatusPreconditionFailed:
		return &Error{Kind: KindProtocolIncompatible, Detail: fmt.Sprintf("status %d", status)}
	case status == http.StatusNotFound:
		return &Error{Kind: KindResourceNotFound, Detail: "status 404"}
	case status == http.StatusConflict:
		return &Error{Kind: KindResourceConflict, Detail: "status 409"}
	case status >= 300 && status < 400:
		// CheckRedirect stopped following; a Node API is never expected to
		// redirect its own JSON endpoints, so treat this as malformed.
		return &Error{Kind: KindMalformedResponse, Detail: fmt.Sprintf("unexpected redirect status %d", status)}
	case status >= 400:
		return &Error{Kind: KindMalformedResponse, Detail: fmt.Sprintf("status %d", status)}
	}
	return nil
}

// roundTrip is used by the provisioning calls because those endpoints return
// a typed application error body that must remain distinguishable from a
// transport/status error. Ordinary remote-management calls continue to use
// doRaw and its stricter status classification.
func (c *Client) roundTrip(ctx context.Context, method, endpoint, requestPath, credential string, body io.Reader) (int, []byte, error) {
	reqCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, method, endpoint+requestPath, body)
	if err != nil {
		return 0, nil, &Error{Kind: KindUnreachable, Detail: "build request"}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, &Error{Kind: KindUnreachable, Detail: classifyTransportError(err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return 0, nil, &Error{Kind: KindUnreachable, Detail: "read response"}
	}
	if len(data) > MaxResponseBytes {
		return 0, nil, &Error{Kind: KindOversizedResponse, Detail: "response exceeded size limit"}
	}
	return resp.StatusCode, data, nil
}

// classifyTransportError keeps the caller-visible detail free of local file
// paths or other sensitive material a Go network/TLS error can carry, while
// remaining useful in logs (see AGENTS.md item 28/30).
func classifyTransportError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if strings.Contains(err.Error(), "certificate") {
		return "tls verification failed"
	}
	return "connection failed"
}
