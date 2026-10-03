package minecraft

import (
	"context"
	"crypto/sha1" //nolint:gosec // upstream publishes SHA-1 digests; used for integrity, not security signatures.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// ServerJarName is the fixed file name of the vanilla and Fabric server jar.
	ServerJarName = "server.jar"

	maxArtifactBytes   = 512 << 20
	maxVersionJSONSize = 8 << 20
	installerTimeout   = 20 * time.Minute

	PhaseResolving   = "resolving"
	PhaseDownloading = "downloading"
	PhaseInstalling  = "installing"
)

var sha1Pattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// Plan is the entire user-controlled input of an installation: a loader and
// exact versions. Nothing else (URL, flag, path, command) is configurable.
type Plan struct {
	Loader           string `json:"loader"`
	MinecraftVersion string `json:"minecraft_version"`
	LoaderVersion    string `json:"loader_version,omitempty"`
}

// Validate checks the plan's shape without contacting any network source.
func (p Plan) Validate() error {
	if !ValidLoader(p.Loader) {
		return ErrUnsupportedLoader
	}
	if !ValidMinecraftVersion(p.MinecraftVersion) {
		return ErrInvalidVersion
	}
	if p.Loader == LoaderVanilla {
		if p.LoaderVersion != "" {
			return ErrInvalidVersion
		}
		return nil
	}
	if !ValidLoaderVersion(p.LoaderVersion) {
		return ErrInvalidVersion
	}
	if p.Loader == LoaderForge && !forgeSupported(p.MinecraftVersion) {
		return fmt.Errorf("%w: Forge is supported for Minecraft 1.17 and newer", ErrInvalidVersion)
	}
	if p.Loader == LoaderNeoForge {
		if mc, ok := neoForgeMinecraftVersion(p.LoaderVersion); !ok || mc != p.MinecraftVersion {
			return fmt.Errorf("%w: NeoForge build does not belong to this Minecraft version", ErrInvalidVersion)
		}
	}
	return nil
}

// Event reports installation progress.
type Event struct{ Phase, Summary string }

// Installer downloads and installs a server into an already-reserved server
// root. It never contacts a source other than the compiled endpoints, verifies
// the published digest when upstream provides one, and invokes Java only as
// exec.CommandContext(java, "-jar", <downloaded installer>, <fixed flag>).
type Installer struct {
	source *Source
	java   func() (string, bool)
}

// NewInstaller builds an Installer over source.
func NewInstaller(source *Source) *Installer {
	return &Installer{source: source, java: DiscoverJava}
}

// SetJavaForTest replaces Java discovery in tests.
func (i *Installer) SetJavaForTest(discover func() (string, bool)) { i.java = discover }

type artifact struct {
	URL  string
	SHA1 string
}

func (s *Source) resolveArtifact(ctx context.Context, plan Plan) (artifact, error) {
	switch plan.Loader {
	case LoaderVanilla:
		releases, err := s.vanillaReleases(ctx)
		if err != nil {
			return artifact{}, err
		}
		for _, release := range releases {
			if release.ID != plan.MinecraftVersion {
				continue
			}
			data, err := s.get(ctx, release.URL, maxVersionJSONSize)
			if err != nil {
				return artifact{}, err
			}
			var detail struct {
				Downloads struct {
					Server struct {
						URL  string `json:"url"`
						SHA1 string `json:"sha1"`
					} `json:"server"`
				} `json:"downloads"`
			}
			if json.Unmarshal(data, &detail) != nil || detail.Downloads.Server.URL == "" || !sha1Pattern.MatchString(detail.Downloads.Server.SHA1) {
				return artifact{}, ErrSourceUnavailable
			}
			return artifact{URL: detail.Downloads.Server.URL, SHA1: detail.Downloads.Server.SHA1}, nil
		}
		return artifact{}, ErrVersionNotFound
	case LoaderFabric:
		offered, err := s.LoaderVersions(ctx, LoaderFabric, plan.MinecraftVersion)
		if err != nil {
			return artifact{}, err
		}
		found := false
		for _, candidate := range offered {
			if candidate.Version == plan.LoaderVersion {
				found = true
				break
			}
		}
		if !found {
			return artifact{}, ErrVersionNotFound
		}
		data, err := s.get(ctx, s.endpoints.FabricMeta+"/installer", maxMetadataBytes)
		if err != nil {
			return artifact{}, err
		}
		var installers []struct {
			Version string `json:"version"`
			Stable  bool   `json:"stable"`
		}
		if json.Unmarshal(data, &installers) != nil {
			return artifact{}, ErrSourceUnavailable
		}
		for _, installer := range installers {
			if installer.Stable && ValidLoaderVersion(installer.Version) {
				return artifact{URL: s.endpoints.FabricMeta + "/loader/" + url.PathEscape(plan.MinecraftVersion) + "/" + url.PathEscape(plan.LoaderVersion) + "/" + url.PathEscape(installer.Version) + "/server/jar"}, nil
			}
		}
		return artifact{}, ErrSourceUnavailable
	case LoaderNeoForge, LoaderForge:
		base, name := s.endpoints.NeoForgeMaven, "neoforge-"+plan.LoaderVersion
		version := plan.LoaderVersion
		if plan.Loader == LoaderForge {
			version = plan.MinecraftVersion + "-" + plan.LoaderVersion
			base, name = s.endpoints.ForgeMaven, "forge-"+version
		}
		location := base + "/" + url.PathEscape(version) + "/" + name + "-installer.jar"
		digest, err := s.get(ctx, location+".sha1", 1024)
		if err != nil {
			return artifact{}, err
		}
		fields := strings.Fields(string(digest))
		if len(fields) == 0 || !sha1Pattern.MatchString(fields[0]) {
			return artifact{}, ErrSourceUnavailable
		}
		return artifact{URL: location, SHA1: fields[0]}, nil
	}
	return artifact{}, ErrUnsupportedLoader
}

// Install installs plan into root. root must already exist and be the managed
// server root; installation writes only inside it.
func (i *Installer) Install(ctx context.Context, root string, plan Plan, output io.Writer, observe func(Event)) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if observe == nil {
		observe = func(Event) {}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return errors.New("server root must be an existing directory")
	}
	observe(Event{PhaseResolving, "Resolving the requested Minecraft server version"})
	resolved, err := i.source.resolveArtifact(ctx, plan)
	if err != nil {
		return err
	}
	observe(Event{PhaseDownloading, "Downloading the Minecraft server files"})
	if plan.Loader == LoaderVanilla || plan.Loader == LoaderFabric {
		return i.source.download(ctx, resolved, filepath.Join(root, ServerJarName))
	}
	java, found := i.java()
	if !found {
		return ErrJavaNotFound
	}
	installerPath := filepath.Join(root, ".gamenode-installer.jar")
	defer os.Remove(installerPath)
	if err = i.source.download(ctx, resolved, installerPath); err != nil {
		return err
	}
	observe(Event{PhaseInstalling, "Running the loader installer (downloads libraries; this can take several minutes)"})
	flag := "--installServer"
	if plan.Loader == LoaderNeoForge {
		flag = "--install-server"
	}
	runCtx, cancel := context.WithTimeout(ctx, installerTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, java, "-jar", installerPath, flag)
	command.Dir = root
	command.Stdout, command.Stderr = output, output
	command.WaitDelay = 10 * time.Second
	if err = command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrInstallerFailed, err)
	}
	_ = os.Remove(filepath.Join(root, "installer.log"))
	return nil
}

var (
	ErrJavaNotFound    = errors.New("Java runtime not found")
	ErrInstallerFailed = errors.New("loader installer failed")
	ErrDigestMismatch  = errors.New("downloaded file does not match the published digest")
)

// download streams the artifact into a temporary sibling, verifies its digest
// when one is published, then atomically renames it into place.
func (s *Source) download(ctx context.Context, item artifact, destination string) error {
	target, err := url.Parse(item.URL)
	if err != nil || s.allowed(target) != nil {
		return ErrSourceUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return ErrSourceUnavailable
	}
	request.Header.Set("User-Agent", "GameNode")
	downloadClient := *s.client
	downloadClient.Timeout = 15 * time.Minute
	response, err := downloadClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSourceUnavailable, redactError(err))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrVersionNotFound
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ErrSourceUnavailable, response.StatusCode)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".gamenode-download-*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporary.Name())
		}
	}()
	hash := sha1.New() //nolint:gosec
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, maxArtifactBytes+1))
	if err != nil {
		return err
	}
	if written > maxArtifactBytes || written == 0 {
		return ErrSourceUnavailable
	}
	if item.SHA1 != "" && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), item.SHA1) {
		return ErrDigestMismatch
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporary.Name(), destination); err != nil {
		return err
	}
	committed = true
	return nil
}
