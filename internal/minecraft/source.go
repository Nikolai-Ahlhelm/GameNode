// Package minecraft is the transport-free Minecraft Java Edition domain:
// fixed-source version discovery, bounded server installation for the vanilla,
// NeoForge, Forge and Fabric loaders, structured launch resolution, and
// read-only/sandboxed mod jar management.
//
// Every download source is compiled into this package. Callers (templates,
// users, the API) only choose a loader plus a version; they can never supply a
// URL, host, flag, or command. See docs/adr/0013-minecraft-loaders-and-mods.md.
package minecraft

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	LoaderVanilla  = "vanilla"
	LoaderNeoForge = "neoforge"
	LoaderForge    = "forge"
	LoaderFabric   = "fabric"

	maxMetadataBytes = 16 << 20
)

// Loaders is the closed, ordered list of supported server types.
var Loaders = []string{LoaderVanilla, LoaderNeoForge, LoaderForge, LoaderFabric}

var (
	minecraftVersionPattern = regexp.MustCompile(`^\d{1,2}\.\d{1,2}(\.\d{1,2})?$`)
	loaderVersionPattern    = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)

	ErrUnsupportedLoader = errors.New("unsupported Minecraft loader")
	ErrInvalidVersion    = errors.New("invalid Minecraft or loader version")
	ErrVersionNotFound   = errors.New("requested version is not offered by the upstream source")
	ErrSourceUnavailable = errors.New("upstream version source is unavailable")
)

// ValidLoader reports whether loader is one of the supported server types.
func ValidLoader(loader string) bool {
	for _, candidate := range Loaders {
		if candidate == loader {
			return true
		}
	}
	return false
}

// ValidMinecraftVersion accepts release-style versions only ("1.21.1", "26.3");
// snapshots and pre-releases are intentionally not installable.
func ValidMinecraftVersion(version string) bool { return minecraftVersionPattern.MatchString(version) }

// ValidLoaderVersion constrains a loader build identifier to a safe token so it
// can be embedded in an upstream URL path segment and a local relative path.
func ValidLoaderVersion(version string) bool { return loaderVersionPattern.MatchString(version) }

// Endpoints are the compiled upstream locations. The type is exported only so
// tests can substitute an httptest server; production code uses
// DefaultEndpoints and there is no runtime configuration surface.
type Endpoints struct {
	MojangManifest string
	NeoForgeAPI    string
	NeoForgeMaven  string
	ForgeMetadata  string
	ForgeMaven     string
	FabricMeta     string
	// AllowedHosts bounds every request and every redirect.
	AllowedHosts []string
	// AllowInsecure is for loopback test servers only.
	AllowInsecure bool
}

// DefaultEndpoints returns the fixed official sources.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		MojangManifest: "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json",
		NeoForgeAPI:    "https://maven.neoforged.net/api/maven/versions/releases/net/neoforged/neoforge",
		NeoForgeMaven:  "https://maven.neoforged.net/releases/net/neoforged/neoforge",
		ForgeMetadata:  "https://maven.minecraftforge.net/net/minecraftforge/forge/maven-metadata.xml",
		ForgeMaven:     "https://maven.minecraftforge.net/net/minecraftforge/forge",
		FabricMeta:     "https://meta.fabricmc.net/v2/versions",
		AllowedHosts: []string{
			"piston-meta.mojang.com", "piston-data.mojang.com", "launcher.mojang.com",
			"maven.neoforged.net", "maven.minecraftforge.net", "meta.fabricmc.net",
		},
	}
}

// Source discovers installable versions and resolves artifact locations.
type Source struct {
	endpoints Endpoints
	client    *http.Client
}

// NewSource builds a Source over the fixed official endpoints.
func NewSource() *Source { return newSource(DefaultEndpoints()) }

func newSource(endpoints Endpoints) *Source {
	source := &Source{endpoints: endpoints}
	source.client = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return source.allowed(request.URL)
	}}
	return source
}

// NewTestSource is for tests in this and dependent packages.
func NewTestSource(endpoints Endpoints) *Source { return newSource(endpoints) }

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

func (s *Source) get(ctx context.Context, location string, limit int64) ([]byte, error) {
	target, err := url.Parse(location)
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
		return nil, fmt.Errorf("%w: %v", ErrSourceUnavailable, redactError(err))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrVersionNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrSourceUnavailable, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrSourceUnavailable
	}
	return data, nil
}

func redactError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

// GameVersions lists release Minecraft versions (newest first) that the loader
// offers at least one installable build for.
func (s *Source) GameVersions(ctx context.Context, loader string) ([]string, error) {
	switch loader {
	case LoaderVanilla:
		releases, err := s.vanillaReleases(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]string, 0, len(releases))
		for _, release := range releases {
			result = append(result, release.ID)
		}
		return result, nil
	case LoaderFabric:
		data, err := s.get(ctx, s.endpoints.FabricMeta+"/game", maxMetadataBytes)
		if err != nil {
			return nil, err
		}
		var games []struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		}
		if json.Unmarshal(data, &games) != nil {
			return nil, ErrSourceUnavailable
		}
		result := []string{}
		for _, game := range games {
			if game.Stable && ValidMinecraftVersion(game.Version) {
				result = append(result, game.Version)
			}
		}
		return result, nil
	case LoaderNeoForge, LoaderForge:
		builds, err := s.loaderBuilds(ctx, loader)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		result := []string{}
		for _, build := range builds {
			if !seen[build.Minecraft] {
				seen[build.Minecraft] = true
				result = append(result, build.Minecraft)
			}
		}
		sortVersionsDescending(result)
		return result, nil
	}
	return nil, ErrUnsupportedLoader
}

// LoaderVersion is one installable loader build for a Minecraft version.
type LoaderVersion struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
	Latest  bool   `json:"latest,omitempty"`
}

// LoaderVersions lists loader builds for minecraftVersion, newest first. Vanilla
// has no separate loader build.
func (s *Source) LoaderVersions(ctx context.Context, loader, minecraftVersion string) ([]LoaderVersion, error) {
	if !ValidMinecraftVersion(minecraftVersion) {
		return nil, ErrInvalidVersion
	}
	switch loader {
	case LoaderVanilla:
		return []LoaderVersion{}, nil
	case LoaderFabric:
		data, err := s.get(ctx, s.endpoints.FabricMeta+"/loader/"+url.PathEscape(minecraftVersion), maxMetadataBytes)
		if err != nil {
			return nil, err
		}
		var entries []struct {
			Loader struct {
				Version string `json:"version"`
				Stable  bool   `json:"stable"`
			} `json:"loader"`
		}
		if json.Unmarshal(data, &entries) != nil {
			return nil, ErrSourceUnavailable
		}
		result := make([]LoaderVersion, 0, len(entries))
		for _, entry := range entries {
			if ValidLoaderVersion(entry.Loader.Version) {
				result = append(result, LoaderVersion{Version: entry.Loader.Version, Stable: entry.Loader.Stable})
			}
		}
		markLatest(result)
		return result, nil
	case LoaderNeoForge, LoaderForge:
		builds, err := s.loaderBuilds(ctx, loader)
		if err != nil {
			return nil, err
		}
		result := []LoaderVersion{}
		for _, build := range builds {
			if build.Minecraft == minecraftVersion {
				result = append(result, LoaderVersion{Version: build.Loader, Stable: build.Stable})
			}
		}
		for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
			result[left], result[right] = result[right], result[left]
		}
		markLatest(result)
		return result, nil
	}
	return nil, ErrUnsupportedLoader
}

func markLatest(versions []LoaderVersion) {
	for index := range versions {
		if versions[index].Stable {
			versions[index].Latest = true
			return
		}
	}
}

type loaderBuild struct {
	Minecraft string
	Loader    string
	Stable    bool
}

// loaderBuilds parses maven metadata. Maven lists versions oldest first.
func (s *Source) loaderBuilds(ctx context.Context, loader string) ([]loaderBuild, error) {
	builds := []loaderBuild{}
	switch loader {
	case LoaderNeoForge:
		data, err := s.get(ctx, s.endpoints.NeoForgeAPI, maxMetadataBytes)
		if err != nil {
			return nil, err
		}
		var listing struct {
			Versions []string `json:"versions"`
		}
		if json.Unmarshal(data, &listing) != nil {
			return nil, ErrSourceUnavailable
		}
		for _, version := range listing.Versions {
			if mc, ok := neoForgeMinecraftVersion(version); ok && ValidLoaderVersion(version) {
				builds = append(builds, loaderBuild{Minecraft: mc, Loader: version, Stable: !strings.Contains(version, "-")})
			}
		}
	case LoaderForge:
		data, err := s.get(ctx, s.endpoints.ForgeMetadata, maxMetadataBytes)
		if err != nil {
			return nil, err
		}
		var metadata struct {
			Versions []string `xml:"versioning>versions>version"`
		}
		if xml.Unmarshal(data, &metadata) != nil {
			return nil, ErrSourceUnavailable
		}
		for _, version := range metadata.Versions {
			mc, build, ok := strings.Cut(version, "-")
			if !ok || !ValidMinecraftVersion(mc) || !ValidLoaderVersion(build) || !forgeSupported(mc) {
				continue
			}
			builds = append(builds, loaderBuild{Minecraft: mc, Loader: build, Stable: true})
		}
	default:
		return nil, ErrUnsupportedLoader
	}
	return builds, nil
}

// forgeSupported limits Forge to the modern installer layout (generated
// libraries/.../*_args.txt argument files, Minecraft 1.17 and newer). Older
// Forge launches a patched jar with a different, non-argfile launch contract
// that GameNode deliberately does not support.
func forgeSupported(minecraftVersion string) bool {
	major, minor, _ := splitMinecraft(minecraftVersion)
	return major > 1 || minor >= 17
}

func splitMinecraft(version string) (int, int, int) {
	parts := strings.Split(version, ".")
	numbers := [3]int{}
	for index := 0; index < len(parts) && index < 3; index++ {
		numbers[index], _ = strconv.Atoi(parts[index])
	}
	return numbers[0], numbers[1], numbers[2]
}

// neoForgeMinecraftVersion maps a NeoForge build number to its Minecraft
// version. Builds for 1.20.2-1.21.x are "<mc minor>.<mc patch>.<build>"
// (21.1.77 => 1.21.1, 21.0.5 => 1.21); builds for the 2026+ year-based
// Minecraft versions are "<year>.<drop>.<patch>.<build>" (26.3.0.45 => 26.3).
func neoForgeMinecraftVersion(version string) (string, bool) {
	core := strings.SplitN(version, "-", 2)[0]
	parts := strings.Split(core, ".")
	numbers := make([]int, len(parts))
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return "", false
		}
		numbers[index] = value
	}
	switch {
	case len(numbers) == 3 && numbers[0] >= 20 && numbers[0] < 26:
		if numbers[1] == 0 {
			return fmt.Sprintf("1.%d", numbers[0]), true
		}
		return fmt.Sprintf("1.%d.%d", numbers[0], numbers[1]), true
	case len(numbers) == 4 && numbers[0] >= 26:
		if numbers[2] == 0 {
			return fmt.Sprintf("%d.%d", numbers[0], numbers[1]), true
		}
		return fmt.Sprintf("%d.%d.%d", numbers[0], numbers[1], numbers[2]), true
	}
	return "", false
}

type vanillaRelease struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	SHA1     string `json:"sha1"`
	Released string `json:"releaseTime"`
}

func (s *Source) vanillaReleases(ctx context.Context) ([]vanillaRelease, error) {
	data, err := s.get(ctx, s.endpoints.MojangManifest, maxMetadataBytes)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Versions []vanillaRelease `json:"versions"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return nil, ErrSourceUnavailable
	}
	releases := []vanillaRelease{}
	for _, version := range manifest.Versions {
		if version.Type == "release" && ValidMinecraftVersion(version.ID) {
			releases = append(releases, version)
		}
	}
	return releases, nil
}

func sortVersionsDescending(versions []string) {
	sort.SliceStable(versions, func(left, right int) bool {
		la, lb, lc := splitMinecraft(versions[left])
		ra, rb, rc := splitMinecraft(versions[right])
		if la != ra {
			return la > ra
		}
		if lb != rb {
			return lb > rb
		}
		return lc > rc
	})
}

// RequiredJavaMajor reports the minimum Java major version Mojang requires to
// run a given Minecraft server version. It is advisory (shown to operators);
// the JVM itself remains the authority.
func RequiredJavaMajor(minecraftVersion string) int {
	major, minor, patch := splitMinecraft(minecraftVersion)
	switch {
	case major >= 26:
		return 25
	case minor >= 21 || (minor == 20 && patch >= 5):
		return 21
	case minor >= 18:
		return 17
	case minor == 17:
		return 16
	}
	return 8
}
