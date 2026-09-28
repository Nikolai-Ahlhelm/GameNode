package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The release source is fixed in code, exactly like the Official Game Library
// and SteamCMD sources (see AGENTS.md sections 11 and 13). There is no
// configurable repository, mirror, URL, or token: an update can only ever be
// fetched from this project's own published GitHub releases.
const (
	Owner      = "Nikolai-Ahlhelm"
	Repository = "GameNode"

	githubAPIBase      = "https://api.github.com"
	githubDownloadBase = "https://github.com"

	// MaxBinaryBytes bounds a downloaded GameNode binary. Release binaries are
	// tens of megabytes; this leaves generous headroom without allowing an
	// unbounded stream to fill the disk.
	MaxBinaryBytes int64 = 256 << 20
	// MaxChecksumBytes bounds the SHA256SUMS.txt manifest.
	MaxChecksumBytes int64 = 64 << 10
	// MaxNotesBytes bounds the release notes retained for display.
	MaxNotesBytes = 16 << 10

	maxAPIResponseBytes = 1 << 20
	apiTimeout          = 20 * time.Second
	maxRedirects        = 5
)

// SourceError is the only error kind a Source returns. Code is stable and
// safe to show to an administrator; raw transport errors, URLs, and response
// bodies never leave this package.
type SourceError struct {
	Code    string
	Message string
}

func (e *SourceError) Error() string { return e.Code + ": " + e.Message }

var (
	errUnavailable = &SourceError{Code: "source_unavailable", Message: "the GameNode release source could not be reached"}
	errRateLimited = &SourceError{Code: "source_rate_limited", Message: "the GameNode release source is rate limiting requests; try again later"}
	errMalformed   = &SourceError{Code: "source_malformed", Message: "the GameNode release source returned an unexpected response"}
	errNoRelease   = &SourceError{Code: "no_release", Message: "no published GameNode release was found"}
	errTooLarge    = &SourceError{Code: "download_too_large", Message: "the release asset exceeds the allowed size"}
)

// Asset describes one published file of a release. Only its name and declared
// size are retained; the download URL is never taken from the API response.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Release is the bounded, validated projection of a GitHub release.
type Release struct {
	Tag         string    `json:"tag"`
	Version     string    `json:"version"`
	Name        string    `json:"name,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	Notes       string    `json:"notes,omitempty"`
	URL         string    `json:"url"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []Asset   `json:"assets"`
}

// Asset looks up a declared asset by exact name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

// Source is the network boundary of the updater. Tests substitute a fake; the
// production implementation is GitHubSource.
type Source interface {
	// Latest returns the newest published, non-draft, non-prerelease release.
	Latest(ctx context.Context) (Release, error)
	// Open streams one asset of a release, refusing anything larger than
	// maxBytes. The returned size is the declared Content-Length or -1.
	Open(ctx context.Context, release Release, asset string, maxBytes int64) (io.ReadCloser, int64, error)
}

// GitHubSource reads this project's releases from GitHub.
type GitHubSource struct {
	client       *http.Client
	apiBase      string
	downloadBase string
	userAgent    string
}

// NewGitHubSource builds the production source. Redirects are followed only
// over HTTPS to GitHub-owned hosts (release assets are served from
// *.githubusercontent.com), so a compromised or misbehaving endpoint cannot
// bounce the download to an arbitrary host.
func NewGitHubSource(version string) *GitHubSource {
	return &GitHubSource{
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       30 * time.Second,
			},
			CheckRedirect: redirectPolicy,
		},
		apiBase:      githubAPIBase,
		downloadBase: githubDownloadBase,
		userAgent:    "GameNode-Updater/" + sanitizeUserAgent(version),
	}
}

func sanitizeUserAgent(version string) string {
	if len(version) > 32 {
		version = version[:32]
	}
	var b strings.Builder
	for _, r := range version {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '+' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "dev"
	}
	return b.String()
}

func redirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" || !allowedHost(req.URL.Hostname()) {
		return errors.New("redirect target is not an allowed release host")
	}
	return nil
}

func allowedHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "api.github.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

type apiRelease struct {
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	Body        string     `json:"body"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	PublishedAt *time.Time `json:"published_at"`
	Assets      []Asset    `json:"assets"`
}

func (g *GitHubSource) Latest(ctx context.Context) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", g.apiBase, Owner, Repository)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, errUnavailable
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", g.userAgent)
	resp, err := g.client.Do(req)
	if err != nil {
		return Release{}, errUnavailable
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Release{}, errNoRelease
	case resp.StatusCode == http.StatusTooManyRequests || (resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0"):
		return Release{}, errRateLimited
	case resp.StatusCode != http.StatusOK:
		return Release{}, errUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return Release{}, errUnavailable
	}
	if len(data) > maxAPIResponseBytes {
		return Release{}, errMalformed
	}
	var raw apiRelease
	if err := json.Unmarshal(data, &raw); err != nil {
		return Release{}, errMalformed
	}
	return g.project(raw)
}

// project validates an API release into the bounded Release value. A draft, a
// prerelease, or a tag that is not a strict semantic-version tag is rejected:
// the tag is later interpolated into a download URL and must never carry
// anything but that fixed shape.
func (g *GitHubSource) project(raw apiRelease) (Release, error) {
	if raw.Draft || raw.Prerelease || !ValidTag(raw.TagName) {
		return Release{}, errNoRelease
	}
	version, err := ParseVersion(raw.TagName)
	if err != nil {
		return Release{}, errMalformed
	}
	out := Release{
		Tag:        raw.TagName,
		Version:    version.String(),
		Name:       truncateText(raw.Name, 200),
		Notes:      truncateText(raw.Body, MaxNotesBytes),
		URL:        fmt.Sprintf("%s/%s/%s/releases/tag/%s", githubDownloadBase, Owner, Repository, raw.TagName),
		Prerelease: raw.Prerelease,
	}
	if raw.PublishedAt != nil {
		out.PublishedAt = raw.PublishedAt.UTC()
	}
	seen := map[string]bool{}
	for _, asset := range raw.Assets {
		if asset.Name == "" || len(asset.Name) > 200 || asset.Size < 0 || seen[asset.Name] {
			continue
		}
		seen[asset.Name] = true
		out.Assets = append(out.Assets, Asset{Name: asset.Name, Size: asset.Size})
	}
	return out, nil
}

func truncateText(text string, limit int) string {
	text = strings.ToValidUTF8(text, "")
	if len(text) <= limit {
		return text
	}
	cut := text[:limit]
	// Never split a multi-byte rune.
	cut = strings.ToValidUTF8(cut, "")
	return cut
}

func (g *GitHubSource) Open(ctx context.Context, release Release, asset string, maxBytes int64) (io.ReadCloser, int64, error) {
	if !ValidTag(release.Tag) {
		return nil, 0, errMalformed
	}
	declared, ok := release.Asset(asset)
	if !ok {
		return nil, 0, errMalformed
	}
	if declared.Size > maxBytes {
		return nil, 0, errTooLarge
	}
	endpoint := fmt.Sprintf("%s/%s/%s/releases/download/%s/%s", g.downloadBase, Owner, Repository, url.PathEscape(release.Tag), url.PathEscape(asset))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, errUnavailable
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", g.userAgent)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, errUnavailable
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, 0, errRateLimited
		}
		return nil, 0, errUnavailable
	}
	if resp.ContentLength > maxBytes {
		_ = resp.Body.Close()
		return nil, 0, errTooLarge
	}
	return &boundedBody{body: resp.Body, remaining: maxBytes}, resp.ContentLength, nil
}

// boundedBody fails, rather than silently truncating, when a stream exceeds
// its limit so an oversized download cannot pass as a shorter valid file.
type boundedBody struct {
	body      io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return n, errTooLarge
	}
	return n, err
}

func (b *boundedBody) Close() error { return b.body.Close() }
