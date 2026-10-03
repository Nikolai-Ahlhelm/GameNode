package vintagestory

import (
	"context"
	"crypto/md5" //nolint:gosec // the upstream index publishes MD5 digests; used for integrity, not signatures.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gamenode/internal/steamcmd"
)

const (
	// ServerDLL is the entry assembly every supported server version ships.
	ServerDLL = "VintagestoryServer.dll"
	// DataDirectory is the server-root-relative --dataPath: world, config, logs and mods.
	DataDirectory = "data"

	maxArchiveBytes = 512 << 20
	// The archive holds ~10.7k files / ~300 MiB; these bounds leave headroom
	// without being unbounded.
	maxExtractEntries = 50000
	maxExtractBytes   = 2 << 30

	PhaseResolving   = "resolving"
	PhaseDownloading = "downloading"
	PhaseExtracting  = "extracting"
)

var (
	ErrDigestMismatch   = errors.New("downloaded archive does not match the published digest")
	ErrExtractFailed    = errors.New("server archive could not be extracted")
	ErrIncompleteServer = errors.New("extracted server is missing required files")
	ErrRuntimeMissing   = errors.New(".NET runtime required by this server version was not found")
)

// Plan is the entire user-controlled input of an installation: one version.
type Plan struct {
	Version string `json:"version"`
}

// Validate checks the plan's shape without any network access.
func (p Plan) Validate() error {
	if !ValidVersion(p.Version) || compareVersions(p.Version, MinimumVersion) < 0 {
		return ErrInvalidVersion
	}
	return nil
}

// Event reports installation progress.
type Event struct{ Phase, Summary string }

// Installer downloads, verifies and extracts the server archive into an
// already-reserved server root. It contacts only the compiled endpoints.
type Installer struct {
	source *Source
	dotnet func() (string, bool)
	majors func() map[int]bool
}

// NewInstaller builds an Installer over source.
func NewInstaller(source *Source) *Installer {
	return &Installer{source: source, dotnet: DiscoverDotnet, majors: InstalledRuntimeMajors}
}

// SetRuntimeForTest replaces runtime discovery in tests.
func (i *Installer) SetRuntimeForTest(dotnet func() (string, bool), majors func() map[int]bool) {
	i.dotnet, i.majors = dotnet, majors
}

// Install installs plan into root.
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
	observe(Event{PhaseResolving, "Resolving the requested Vintage Story version"})
	artifact, err := i.source.Resolve(ctx, plan.Version)
	if err != nil {
		return err
	}
	observe(Event{PhaseDownloading, "Downloading the Vintage Story server archive"})
	archive, err := os.CreateTemp(root, ".gamenode-download-*")
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)
	err = i.source.download(ctx, artifact, archive)
	_ = archive.Close()
	if err != nil {
		return err
	}
	observe(Event{PhaseExtracting, "Extracting the server files"})
	if err = steamcmd.ExtractWithLimits(artifact.Kind, archivePath, root, steamcmd.ExtractLimits{MaxEntries: maxExtractEntries, MaxBytes: maxExtractBytes}); err != nil {
		return fmt.Errorf("%w: %v", ErrExtractFailed, err)
	}
	if _, err = os.Stat(filepath.Join(root, ServerDLL)); err != nil {
		return ErrIncompleteServer
	}
	required, err := RequiredRuntimeMajor(root)
	if err != nil {
		return ErrIncompleteServer
	}
	if _, found := i.dotnet(); !found || !i.majors()[required] {
		return fmt.Errorf("%w: install .NET %d", ErrRuntimeMissing, required)
	}
	return nil
}

func (s *Source) download(ctx context.Context, artifact Artifact, destination *os.File) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return ErrSourceUnavailable
	}
	request.Header.Set("User-Agent", "GameNode")
	client := *s.client
	client.Timeout = 20 * time.Minute
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSourceUnavailable, errors.Unwrap(err))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ErrVersionNotFound
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: status %d", ErrSourceUnavailable, response.StatusCode)
	}
	hash := md5.New() //nolint:gosec
	written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		return err
	}
	if written > maxArchiveBytes || written == 0 {
		return ErrSourceUnavailable
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.MD5) {
		return ErrDigestMismatch
	}
	return nil
}

// RequiredRuntimeMajor reads the .NET major version the installed server needs
// from its runtimeconfig.json.
func RequiredRuntimeMajor(root string) (int, error) {
	data, err := os.ReadFile(filepath.Join(root, "VintagestoryServer.runtimeconfig.json"))
	if err != nil || len(data) > 64<<10 {
		return 0, errors.New("runtime configuration is unavailable")
	}
	var config struct {
		RuntimeOptions struct {
			Framework struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"framework"`
		} `json:"runtimeOptions"`
	}
	if json.Unmarshal(data, &config) != nil || config.RuntimeOptions.Framework.Name != "Microsoft.NETCore.App" {
		return 0, errors.New("runtime configuration is unsupported")
	}
	major := 0
	if _, err = fmt.Sscanf(config.RuntimeOptions.Framework.Version, "%d.", &major); err != nil || major < 5 || major > 99 {
		return 0, errors.New("runtime version is invalid")
	}
	return major, nil
}
