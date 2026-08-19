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
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gamenode/internal/provisioning"
)

// DefaultTimeout bounds every single request this client makes. Callers may
// still supply a shorter context deadline.
const DefaultTimeout = 8 * time.Second

// MaxResponseBytes bounds how much of a Node API response body this client
// will read, regardless of what the remote end claims in Content-Length.
const MaxResponseBytes = 1 << 20 // 1 MiB

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

// ProvisioningRequest is the typed, bounded transport contract for the
// machine-authenticated remote provisioning path
// (POST /api/v1/node/provisioning). Its fields are exactly the fields the
// existing local provisioning API already accepts (see
// internal/api/provisioning.go's provisionInput and
// internal/provisioning.Request) - there is no raw JSON payload, no generic
// map of engine flags, and no field for mounts/devices/capabilities/host
// networking/registry credentials/installer scripts. A target node applies
// every validation it already applies to a local request (template/Egg
// compatibility, image allowlist, resource limits, tenant sandbox) before
// this ever reaches its container runtime; this client never bypasses any
// of that by construction, because it can only send these fields.
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

// StartProvisioning submits a provisioning request to a Remote Node's own
// provisioning.Service via the machine-authenticated Node API. The target
// node remains the sole authority for template/Egg validation, the image
// allowlist, resource limits, its tenant/filesystem sandbox, the container
// installer, and job persistence - this call only ever reaches its existing
// POST /api/v1/node/provisioning endpoint, which itself only ever calls the
// node's local provisioning.Service.Start (see
// docs/adr/0009-cluster-scheduling-decision-vs-execution.md). The returned
// Job is the same typed value the node's local provisioning API returns.
func (c *Client) StartProvisioning(ctx context.Context, endpoint, credential string, req ProvisioningRequest) (provisioning.Job, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return provisioning.Job{}, &Error{Kind: KindMalformedResponse, Detail: "encode provisioning request"}
	}
	var job provisioning.Job
	err = c.doProvisioning(ctx, http.MethodPost, endpoint, "/api/v1/node/provisioning", credential, bytes.NewReader(body), &job)
	return job, err
}

// GetProvisioningJob mirrors the node's local provisioning job status read
// (GET /api/v1/provisioning/jobs/{id}) over the machine-authenticated Node
// API. No second job-tracking mechanism is introduced anywhere - the
// returned Job is read straight from the target node's own
// provisioning.Store via its existing provisioning.Service.Get.
func (c *Client) GetProvisioningJob(ctx context.Context, endpoint, credential, jobID string) (provisioning.Job, error) {
	var job provisioning.Job
	err := c.doProvisioning(ctx, http.MethodGet, endpoint, "/api/v1/node/provisioning/"+url.PathEscape(jobID), credential, nil, &job)
	return job, err
}

// CancelProvisioningJob mirrors the node's local provisioning job cancel
// (POST /api/v1/provisioning/jobs/{id}/cancel) over the machine-authenticated
// Node API, delegating to the same provisioning.Service.Cancel the node's own
// operators use.
func (c *Client) CancelProvisioningJob(ctx context.Context, endpoint, credential, jobID string) (provisioning.Job, error) {
	var job provisioning.Job
	err := c.doProvisioning(ctx, http.MethodPost, endpoint, "/api/v1/node/provisioning/"+url.PathEscape(jobID)+"/cancel", credential, nil, &job)
	return job, err
}

// ProvisioningError is a typed, sanitized mirror of the target node's own
// provisioning error response (the same {"error":{"code","message"}} shape
// internal/api/provisioning.go's errorOut produces for a local caller - see
// provisioningError in that file for the full code list, e.g.
// "not_provisionable", "container_image_policy_blocked",
// "container_image_not_declared", "container_runtime_unavailable",
// "port_conflict", "name_conflict", "target_conflict", "invalid_tenant").
// The controller-side proxy mirrors Code/Message back to its own caller
// unchanged instead of collapsing every 4xx into one generic transport
// error, so a rejected remote container placement is exactly as
// diagnosable as a rejected local one.
type ProvisioningError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ProvisioningError) Error() string { return e.Code + ": " + e.Message }

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// doProvisioning is do's counterpart for the three provisioning calls above:
// it preserves the same transport-error classification (unreachable,
// authentication failed, protocol incompatible, oversized/malformed
// response) but, for an application-level 4xx the target node's
// provisioning API returned, decodes and returns a *ProvisioningError
// instead of collapsing it to KindMalformedResponse - see
// ProvisioningError's doc comment.
func (c *Client) doProvisioning(ctx context.Context, method, endpoint, path, credential string, body io.Reader, out any) error {
	status, data, err := c.roundTrip(ctx, method, endpoint, path, credential, body)
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
		var body errorBody
		if json.Unmarshal(data, &body) == nil && body.Error.Code != "" {
			return &ProvisioningError{StatusCode: status, Code: body.Error.Code, Message: body.Error.Message}
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

func (c *Client) do(ctx context.Context, method, endpoint, path, credential string, body io.Reader, out any) error {
	status, data, err := c.roundTrip(ctx, method, endpoint, path, credential, body)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Kind: KindAuthenticationFailed, Detail: fmt.Sprintf("status %d", status)}
	case status == http.StatusUpgradeRequired || status == http.StatusPreconditionFailed:
		return &Error{Kind: KindProtocolIncompatible, Detail: fmt.Sprintf("status %d", status)}
	case status >= 300 && status < 400:
		// CheckRedirect stopped following; a Node API is never expected to
		// redirect its own JSON endpoints, so treat this as malformed.
		return &Error{Kind: KindMalformedResponse, Detail: fmt.Sprintf("unexpected redirect status %d", status)}
	case status >= 400:
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

// roundTrip performs the actual HTTP call shared by do and doProvisioning:
// build the fixed-path request, send it with the bounded client timeout,
// and read a size-bounded response body. It never interprets the status
// code itself - that stays in each caller so do and doProvisioning can
// classify 4xx differently.
func (c *Client) roundTrip(ctx context.Context, method, endpoint, path, credential string, body io.Reader) (int, []byte, error) {
	reqCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, method, endpoint+path, body)
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
	limited := io.LimitReader(resp.Body, MaxResponseBytes+1)
	data, err := io.ReadAll(limited)
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
