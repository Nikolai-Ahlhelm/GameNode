// Package vintagestory is the transport-free Vintage Story domain: fixed-source
// version discovery, a verified server installer, structured launch resolution
// through the .NET runtime, and the mod archive profile. Download sources are
// compiled in; callers choose only an exact version. See
// docs/adr/0014-vintage-story-and-hytale.md.
package vintagestory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MinimumVersion is the oldest server version GameNode installs; it is the
// oldest whose configuration keys and runtime requirements were verified.
const MinimumVersion = "1.21.0"

const (
	ChannelStable = "stable"
	ChannelRC     = "rc"

	maxIndexBytes = 8 << 20
)

var (
	versionPattern = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.(\d{1,3})(?:-rc\.(\d{1,3}))?$`)
	md5Pattern     = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

	ErrInvalidVersion    = errors.New("invalid Vintage Story version")
	ErrVersionNotFound   = errors.New("requested version is not offered by the upstream source")
	ErrSourceUnavailable = errors.New("upstream version source is unavailable")
)

// ValidVersion accepts stable ("1.22.7") and release-candidate ("1.22.0-rc.10")
// versions only.
func ValidVersion(version string) bool { return versionPattern.MatchString(version) }

// Channel reports "stable" or "rc" for a valid version.
func Channel(version string) string {
	if strings.Contains(version, "-rc.") {
		return ChannelRC
	}
	return ChannelStable
}

// Endpoints are the compiled upstream locations; exported only so tests can
// substitute an httptest server.
type Endpoints struct {
	VersionIndex  string
	AllowedHosts  []string
	AllowInsecure bool
}

// DefaultEndpoints returns the fixed official sources.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		VersionIndex: "https://api.vintagestory.at/stable-unstable.json",
		AllowedHosts: []string{"api.vintagestory.at", "cdn.vintagestory.at"},
	}
}

// Version is one installable server version.
type Version struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	// Latest marks the newest stable version.
	Latest bool `json:"latest,omitempty"`
}

// Source discovers versions and resolves archive locations.
type Source struct {
	endpoints Endpoints
	client    *http.Client
}

// NewSource builds a Source over the fixed official endpoints.
func NewSource() *Source { return NewTestSource(DefaultEndpoints()) }

// NewTestSource is for tests in this and dependent packages.
func NewTestSource(endpoints Endpoints) *Source {
	source := &Source{endpoints: endpoints}
	source.client = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return source.allowed(request.URL)
	}}
	return source
}

func (s *Source) allowed(target *url.URL) error {
	if target.Scheme != "https" && !(s.endpoints.AllowInsecure && target.Scheme == "http") {
		return errors.New("only HTTPS upstream locations are permitted")
	}
	if target.User != nil {
		return errors.New("credentials in upstream locations are not permitted")
	}
	host := strings.ToLower(target.Hostname())
	for _, allowed := range s.endpoints.AllowedHosts {
		if host == allowed {
			return nil
		}
	}
	return errors.New("upstream host is not permitted")
}

type indexFile struct {
	Filename string `json:"filename"`
	MD5      string `json:"md5"`
	URLs     struct {
		CDN string `json:"cdn"`
	} `json:"urls"`
}

type indexEntry struct {
	LinuxServer   *indexFile `json:"linuxserver"`
	WindowsServer *indexFile `json:"windowsserver"`
}

func (s *Source) index(ctx context.Context) (map[string]indexEntry, error) {
	target, err := url.Parse(s.endpoints.VersionIndex)
	if err != nil || s.allowed(target) != nil {
		return nil, ErrSourceUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, ErrSourceUnavailable
	}
	request.Header.Set("User-Agent", "GameNode")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSourceUnavailable, errors.Unwrap(err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrSourceUnavailable, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxIndexBytes+1))
	if err != nil || len(data) > maxIndexBytes {
		return nil, ErrSourceUnavailable
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil, ErrSourceUnavailable
	}
	result := map[string]indexEntry{}
	for version, value := range raw {
		if !ValidVersion(version) {
			continue
		}
		var entry indexEntry
		if json.Unmarshal(value, &entry) == nil {
			result[version] = entry
		}
	}
	return result, nil
}

// Versions lists installable stable and release-candidate versions for the
// host platform, newest first.
func (s *Source) Versions(ctx context.Context) ([]Version, error) {
	index, err := s.index(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Version, 0, len(index))
	for version, entry := range index {
		if archiveFor(entry) != nil && compareVersions(version, MinimumVersion) >= 0 {
			result = append(result, Version{Version: version, Channel: Channel(version)})
		}
	}
	sort.Slice(result, func(left, right int) bool { return compareVersions(result[left].Version, result[right].Version) > 0 })
	for index := range result {
		if result[index].Channel == ChannelStable {
			result[index].Latest = true
			break
		}
	}
	return result, nil
}

func archiveFor(entry indexEntry) *indexFile {
	if runtime.GOOS == "windows" {
		return entry.WindowsServer
	}
	return entry.LinuxServer
}

// compareVersions orders by numeric parts, with a release candidate sorting
// below the stable release it precedes.
func compareVersions(left, right string) int {
	l, r := versionPattern.FindStringSubmatch(left), versionPattern.FindStringSubmatch(right)
	if l == nil || r == nil {
		return strings.Compare(left, right)
	}
	for index := 1; index <= 3; index++ {
		a, _ := strconv.Atoi(l[index])
		b, _ := strconv.Atoi(r[index])
		if a != b {
			return a - b
		}
	}
	switch {
	case l[4] == "" && r[4] == "":
		return 0
	case l[4] == "":
		return 1
	case r[4] == "":
		return -1
	}
	a, _ := strconv.Atoi(l[4])
	b, _ := strconv.Atoi(r[4])
	return a - b
}

// Artifact locates and identifies one server archive.
type Artifact struct {
	URL  string
	MD5  string
	Kind string // "zip" or "tar.gz"
}

// Resolve returns the archive for version on the host platform.
func (s *Source) Resolve(ctx context.Context, version string) (Artifact, error) {
	if !ValidVersion(version) {
		return Artifact{}, ErrInvalidVersion
	}
	index, err := s.index(ctx)
	if err != nil {
		return Artifact{}, err
	}
	entry, ok := index[version]
	if !ok {
		return Artifact{}, ErrVersionNotFound
	}
	file := archiveFor(entry)
	if file == nil {
		return Artifact{}, ErrVersionNotFound
	}
	kind, expected := "tar.gz", "vs_server_linux-x64_"+version+".tar.gz"
	if runtime.GOOS == "windows" {
		kind, expected = "zip", "vs_server_win-x64_"+version+".zip"
	}
	target, err := url.Parse(file.URLs.CDN)
	if file.Filename != expected || !md5Pattern.MatchString(file.MD5) || err != nil || s.allowed(target) != nil {
		return Artifact{}, ErrSourceUnavailable
	}
	return Artifact{URL: target.String(), MD5: strings.ToLower(file.MD5), Kind: kind}, nil
}

// RequiredDotnetMajor is the advisory .NET major version a server version
// needs, shown before installation. The authoritative requirement is read from
// the installed server's runtimeconfig.json.
func RequiredDotnetMajor(version string) int {
	match := versionPattern.FindStringSubmatch(version)
	if match == nil {
		return 0
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	switch {
	case major > 1 || minor >= 22:
		return 10
	case minor >= 21:
		return 8
	}
	return 7
}
