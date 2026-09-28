package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testGitHub(t *testing.T, handler http.HandlerFunc) *GitHubSource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &GitHubSource{client: server.Client(), apiBase: server.URL, downloadBase: server.URL, userAgent: "GameNode-Updater/test"}
}

func TestLatestProjectsAndBoundsRelease(t *testing.T) {
	var sawPath, sawUA string
	source := testGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		sawPath, sawUA = r.URL.Path, r.Header.Get("User-Agent")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.2.3", "name": "GameNode 1.2.3", "body": strings.Repeat("n", MaxNotesBytes+500), "draft": false, "prerelease": false,
			"published_at": "2026-01-02T03:04:05Z",
			"assets": []map[string]any{
				{"name": "gamenode-linux-amd64", "size": 100, "browser_download_url": "https://evil.example/steal"},
				{"name": "gamenode-linux-amd64", "size": 5}, // duplicate ignored
				{"name": "SHA256SUMS.txt", "size": 200},
				{"name": "", "size": 1},
			},
		})
	})
	release, err := source.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sawPath != "/repos/Nikolai-Ahlhelm/GameNode/releases/latest" || sawUA != "GameNode-Updater/test" {
		t.Errorf("unexpected request %q %q", sawPath, sawUA)
	}
	if release.Version != "1.2.3" || release.Tag != "v1.2.3" || len(release.Notes) != MaxNotesBytes || len(release.Assets) != 2 {
		t.Errorf("unexpected projection: %+v", release)
	}
	if strings.Contains(release.URL, "evil") || !strings.HasPrefix(release.URL, "https://github.com/Nikolai-Ahlhelm/GameNode/releases/tag/") {
		t.Errorf("release URL must be constructed, not taken from the API: %q", release.URL)
	}
}

func TestLatestRejectsUnsafeReleases(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"draft":      {"tag_name": "v1.0.0", "draft": true},
		"prerelease": {"tag_name": "v1.0.0", "prerelease": true},
		"traversal":  {"tag_name": "v1.0.0/../../evil"},
		"no v":       {"tag_name": "1.0.0"},
		"empty":      {"tag_name": ""},
	} {
		body := body
		source := testGitHub(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(body) })
		if _, err := source.Latest(context.Background()); err == nil {
			t.Errorf("%s: release should have been rejected", name)
		}
	}
}

func TestLatestErrorClassification(t *testing.T) {
	cases := map[int]string{404: "no_release", 500: "source_unavailable", 429: "source_rate_limited"}
	for status, code := range cases {
		status := status
		source := testGitHub(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		_, err := source.Latest(context.Background())
		var sourceErr *SourceError
		if !errors.As(err, &sourceErr) || sourceErr.Code != code {
			t.Errorf("status %d: got %v want %s", status, err, code)
		}
	}
	limited := testGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(403)
	})
	if _, err := limited.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "source_rate_limited") {
		t.Errorf("403 with exhausted limit should be a rate limit: %v", err)
	}
	garbage := testGitHub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "not json") })
	if _, err := garbage.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "source_malformed") {
		t.Errorf("garbage body: %v", err)
	}
}

func TestOpenBuildsFixedURLAndBoundsDownload(t *testing.T) {
	var sawPath string
	source := testGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		_, _ = w.Write([]byte("0123456789"))
	})
	release := Release{Tag: "v1.2.3", Assets: []Asset{{Name: "gamenode-linux-amd64", Size: 10}}}
	body, _, err := source.Open(context.Background(), release, "gamenode-linux-amd64", 10)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(data) != "0123456789" {
		t.Fatalf("exact-limit body: %q %v", data, err)
	}
	if sawPath != "/Nikolai-Ahlhelm/GameNode/releases/download/v1.2.3/gamenode-linux-amd64" {
		t.Errorf("unexpected download path %q", sawPath)
	}
	if body, _, err = source.Open(context.Background(), release, "gamenode-linux-amd64", 9); err == nil {
		_ = body.Close()
		t.Fatal("declared size above the limit must be refused")
	}
	// A server that streams more than the limit must fail, not truncate.
	streaming := testGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 50)))
		w.(http.Flusher).Flush()
	})
	lying := Release{Tag: "v1.2.3", Assets: []Asset{{Name: "SHA256SUMS.txt", Size: 5}}}
	body, _, err = streaming.Open(context.Background(), lying, "SHA256SUMS.txt", 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(body); err == nil {
		t.Error("stream beyond the limit must error")
	}
	_ = body.Close()
	if _, _, err = source.Open(context.Background(), Release{Tag: "v1.2.3/../x"}, "a", 10); err == nil {
		t.Error("invalid tag must be refused")
	}
	if _, _, err = source.Open(context.Background(), release, "not-declared", 10); err == nil {
		t.Error("undeclared asset must be refused")
	}
}

func TestRedirectPolicyAllowsOnlyHTTPSGitHubHosts(t *testing.T) {
	check := func(raw string, via int) error {
		u, _ := url.Parse(raw)
		return redirectPolicy(&http.Request{URL: u}, make([]*http.Request, via))
	}
	for _, ok := range []string{"https://github.com/x", "https://release-assets.githubusercontent.com/x", "https://objects.githubusercontent.com/x", "https://api.github.com/x"} {
		if err := check(ok, 1); err != nil {
			t.Errorf("%s should be allowed: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://github.com/x", "https://evil.example/x", "https://github.com.evil.example/x", "https://notgithubusercontent.com/x", "https://githubusercontent.com.evil.example/x", "ftp://github.com/x"} {
		if err := check(bad, 1); err == nil {
			t.Errorf("%s must be refused", bad)
		}
	}
	if err := check("https://github.com/x", maxRedirects); err == nil {
		t.Error("redirect loops must be cut off")
	}
}
