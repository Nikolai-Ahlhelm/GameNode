package templates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	PelicanTreeURL   = "https://api.github.com/repos/pelican-eggs/games-steamcmd/git/trees/main?recursive=1"
	PelicanRawBase   = "https://raw.githubusercontent.com/pelican-eggs/games-steamcmd/main/"
	MaxPelicanItems  = 256
	PelicanCacheTime = 15 * time.Minute
)

var ErrPelicanUnavailable = errors.New("Pelican Egg catalog is unavailable")

type PelicanItem struct {
	Template Template `json:"template"`
	Path     string   `json:"path"`
}

type PelicanResult struct {
	Source    string        `json:"source"`
	Revision  string        `json:"revision,omitempty"`
	FetchedAt time.Time     `json:"fetched_at,omitempty"`
	Offline   bool          `json:"offline"`
	LastError string        `json:"last_error,omitempty"`
	Templates []PelicanItem `json:"templates"`
}

type pelicanTree struct {
	SHA  string `json:"sha"`
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
}

type PelicanCatalog struct {
	client    *http.Client
	mu        sync.RWMutex
	refreshMu sync.Mutex
	items     []PelicanItem
	result    PelicanResult
	fetched   time.Time
}

func NewPelicanCatalog() *PelicanCatalog {
	client := &http.Client{Timeout: 20 * time.Second}
	client.CheckRedirect = func(r *http.Request, _ []*http.Request) error {
		if r.URL.Scheme != "https" || (r.URL.Host != "api.github.com" && r.URL.Host != "raw.githubusercontent.com") {
			return errors.New("Pelican source redirect rejected")
		}
		return nil
	}
	return &PelicanCatalog{client: client, result: PelicanResult{Source: PelicanTreeURL}}
}

func (c *PelicanCatalog) List(ctx context.Context) (PelicanResult, error) {
	c.mu.RLock()
	result, fetched := c.result, c.fetched
	c.mu.RUnlock()
	if !fetched.IsZero() && time.Since(fetched) < PelicanCacheTime {
		return result, nil
	}
	return c.Refresh(ctx)
}

func (c *PelicanCatalog) Refresh(ctx context.Context) (PelicanResult, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.mu.RLock()
	if !c.fetched.IsZero() && time.Since(c.fetched) < PelicanCacheTime {
		result := c.result
		c.mu.RUnlock()
		return result, nil
	}
	c.mu.RUnlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	treeData, err := c.fetch(ctx, PelicanTreeURL, 2<<20)
	if err != nil {
		return c.failed(err)
	}
	var tree pelicanTree
	if err := json.Unmarshal(treeData, &tree); err != nil || tree.SHA == "" {
		return c.failed(errors.New("Pelican tree response is invalid"))
	}
	paths := make([]string, 0, MaxPelicanItems)
	for _, entry := range tree.Tree {
		if entry.Type == "blob" && strings.HasPrefix(entry.Path, "") && strings.HasSuffix(strings.ToLower(entry.Path), ".json") && strings.Contains(entry.Path, "/egg-") {
			paths = append(paths, entry.Path)
		}
	}
	sort.Strings(paths)
	if len(paths) > MaxPelicanItems {
		paths = paths[:MaxPelicanItems]
	}
	items := make([]PelicanItem, 0, len(paths))
	jobs := make(chan string)
	results := make(chan PelicanItem, len(paths))
	workers := 8
	if len(paths) < workers {
		workers = len(paths)
	}
	if workers == 0 {
		workers = 1
	}
	var workersDone sync.WaitGroup
	for i := 0; i < workers; i++ {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			for path := range jobs {
				data, fetchErr := c.fetch(ctx, PelicanRawBase+path, MaxEggBytes)
				if fetchErr != nil {
					continue
				}
				template, analyzeErr := AnalyzeEgg(data)
				if analyzeErr != nil {
					continue
				}
				template.ID = pelicanID(path, data)
				template.SourceIdentifier = path
				results <- PelicanItem{Template: template, Path: path}
			}
		}()
	}
	go func() {
		for _, path := range paths {
			jobs <- path
		}
		close(jobs)
		workersDone.Wait()
		close(results)
	}()
	for item := range results {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	result := PelicanResult{Source: PelicanTreeURL, Revision: tree.SHA, FetchedAt: time.Now().UTC(), Templates: items}
	c.mu.Lock()
	c.items, c.result, c.fetched = items, result, result.FetchedAt
	c.mu.Unlock()
	return result, nil
}

func (c *PelicanCatalog) Import(ctx context.Context, path string) (Template, error) {
	result, err := c.List(ctx)
	if err != nil {
		return Template{}, err
	}
	for _, item := range result.Templates {
		if item.Path != path {
			continue
		}
		data, fetchErr := c.fetch(ctx, PelicanRawBase+path, MaxEggBytes)
		if fetchErr != nil {
			return Template{}, fetchErr
		}
		template, analyzeErr := AnalyzeEgg(data)
		if analyzeErr != nil {
			return Template{}, fmt.Errorf("Pelican Egg could not be normalized: %w", analyzeErr)
		}
		return template, nil
	}
	return Template{}, errors.New("Pelican Egg path is not in the current catalog")
}

func (c *PelicanCatalog) fetch(ctx context.Context, target string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Pelican source HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("Pelican source response exceeds the size limit")
	}
	return data, nil
}

func (c *PelicanCatalog) failed(err error) (PelicanResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) > 0 {
		result := c.result
		result.Offline, result.LastError = true, "Pelican catalog refresh failed; cached results are shown"
		return result, err
	}
	return PelicanResult{Source: PelicanTreeURL, Offline: true, LastError: "Pelican catalog unavailable"}, err
}

func pelicanID(path string, data []byte) string {
	hash := sha256.Sum256(append([]byte(path+":"), data...))
	return "pelican-" + hex.EncodeToString(hash[:])[:20]
}
