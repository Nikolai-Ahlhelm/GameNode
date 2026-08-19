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

func (c *Client) do(ctx context.Context, method, endpoint, path, credential string, body io.Reader, out any) error {
	reqCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, method, endpoint+path, body)
	if err != nil {
		return &Error{Kind: KindUnreachable, Detail: "build request"}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Kind: KindUnreachable, Detail: classifyTransportError(err)}
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, MaxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return &Error{Kind: KindUnreachable, Detail: "read response"}
	}
	if len(data) > MaxResponseBytes {
		return &Error{Kind: KindOversizedResponse, Detail: "response exceeded size limit"}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &Error{Kind: KindAuthenticationFailed, Detail: fmt.Sprintf("status %d", resp.StatusCode)}
	case resp.StatusCode == http.StatusUpgradeRequired || resp.StatusCode == http.StatusPreconditionFailed:
		return &Error{Kind: KindProtocolIncompatible, Detail: fmt.Sprintf("status %d", resp.StatusCode)}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		// CheckRedirect stopped following; a Node API is never expected to
		// redirect its own JSON endpoints, so treat this as malformed.
		return &Error{Kind: KindMalformedResponse, Detail: fmt.Sprintf("unexpected redirect status %d", resp.StatusCode)}
	case resp.StatusCode >= 400:
		return &Error{Kind: KindMalformedResponse, Detail: fmt.Sprintf("status %d", resp.StatusCode)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &Error{Kind: KindMalformedResponse, Detail: "invalid response body"}
	}
	return nil
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
