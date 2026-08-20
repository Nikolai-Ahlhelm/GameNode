package templates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AnalyzeEggURL deliberately supports only immutable-looking public GitHub
// file links. Arbitrary download URLs would turn Egg import into a generic
// remote-code/data fetcher, which is outside GameNode's trust boundary.
func AnalyzeEggURL(ctx context.Context, rawURL string) (Template, string, error) {
	canonical, err := canonicalEggURL(rawURL)
	if err != nil {
		return Template{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return Template{}, "", errors.New("invalid Egg URL")
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if _, err := canonicalEggURL(req.URL.String()); err != nil {
			return errors.New("redirect leaves the approved GitHub hosts")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return Template{}, "", fmt.Errorf("Egg URL could not be fetched: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Template{}, "", fmt.Errorf("Egg URL returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxEggBytes+1))
	if err != nil || len(data) > MaxEggBytes {
		return Template{}, "", errors.New("Egg URL exceeds the 256 KiB limit")
	}
	template, err := AnalyzeEgg(data)
	if err != nil {
		return Template{}, "", err
	}
	template.SourceIdentifier = canonical
	return template, canonical, nil
}

func canonicalEggURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 512 {
		return "", errors.New("Egg URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return "", errors.New("Egg URL must be an HTTPS GitHub file link without query parameters")
	}
	if strings.Contains(u.Path, "..") || !strings.HasSuffix(strings.ToLower(u.Path), ".json") {
		return "", errors.New("Egg URL must point to a JSON file")
	}
	switch strings.ToLower(u.Hostname()) {
	case "raw.githubusercontent.com":
		return u.String(), nil
	case "github.com":
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 5 || parts[2] != "blob" {
			return "", errors.New("GitHub URL must use the /blob/<revision>/... format")
		}
		return "https://raw.githubusercontent.com/" + strings.Join(append(parts[:2], parts[3:]...), "/"), nil
	default:
		return "", errors.New("only github.com and raw.githubusercontent.com are approved")
	}
}
