package steamcmd

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	WindowsURL              = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip"
	LinuxURL                = "https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz"
	MaxArchiveBytes   int64 = 128 << 20
	MaxExtractedBytes int64 = 512 << 20
	MaxArchiveEntries       = 10000
)

var (
	ErrUnsupportedPlatform   = errors.New("SteamCMD is unsupported on this platform")
	ErrManagedInstallCorrupt = errors.New("managed SteamCMD installation is incomplete")
	ErrInstallFailed         = errors.New("SteamCMD installation failed")
	betaPattern              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

type Platform struct{ OS, URL, Archive, Executable string }

func CurrentPlatform(goos string) (Platform, error) {
	switch goos {
	case "windows":
		return Platform{goos, WindowsURL, "zip", "steamcmd.exe"}, nil
	case "linux":
		return Platform{goos, LinuxURL, "tar.gz", filepath.Join("linux32", "steamcmd")}, nil
	default:
		return Platform{}, ErrUnsupportedPlatform
	}
}

type Downloader interface {
	Download(context.Context, string) error
}
type Command struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	Output           io.Writer
	Environment      map[string]string
}
type Result struct{ ExitCode int }
type Runner interface {
	Run(context.Context, Command) (Result, error)
}
type InstallPlan struct {
	AppID      int
	Validate   bool
	BetaBranch string
	LoginMode  string
}
type Event struct{ Phase, Summary string }
type EventSink func(Event)

type Manager struct {
	root       string
	platform   Platform
	downloader Downloader
	runner     Runner
	bootstrap  sync.Mutex
}

func New(root string, platform Platform, downloader Downloader, runner Runner) *Manager {
	if downloader == nil {
		downloader = &HTTPDownloader{source: platform.URL}
	}
	if runner == nil {
		runner = NativeRunner{}
	}
	return &Manager{root: filepath.Clean(root), platform: platform, downloader: downloader, runner: runner}
}
func (m *Manager) Root() string       { return m.root }
func (m *Manager) Executable() string { return filepath.Join(m.root, m.platform.Executable) }
func (m *Manager) Detect() bool {
	info, err := os.Stat(m.Executable())
	return err == nil && !info.IsDir() && info.Mode().IsRegular()
}

func (m *Manager) Ensure(ctx context.Context, sink EventSink) error {
	m.bootstrap.Lock()
	defer m.bootstrap.Unlock()
	if m.Detect() {
		if sink != nil {
			sink(Event{"steamcmd_ready", "Managed SteamCMD is ready"})
		}
		return nil
	}
	if _, err := os.Stat(m.root); err == nil {
		return ErrManagedInstallCorrupt
	} else if !os.IsNotExist(err) {
		return errors.New("managed tools directory is unavailable")
	}
	if sink != nil {
		sink(Event{"downloading_steamcmd", "Downloading SteamCMD from the trusted Valve source"})
	}
	parent := filepath.Dir(m.root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return errors.New("managed tools directory is unavailable")
	}
	temp, err := os.MkdirTemp(parent, ".steamcmd-bootstrap-")
	if err != nil {
		return errors.New("SteamCMD bootstrap could not start")
	}
	defer os.RemoveAll(temp)
	archive := filepath.Join(temp, "steamcmd.archive")
	if err = m.downloader.Download(ctx, archive); err != nil {
		return fmt.Errorf("SteamCMD download failed: %w", sanitized(err))
	}
	extract := filepath.Join(temp, "content")
	if err = os.Mkdir(extract, 0700); err != nil {
		return errors.New("SteamCMD bootstrap could not prepare extraction")
	}
	if err = Extract(m.platform.Archive, archive, extract); err != nil {
		return fmt.Errorf("SteamCMD archive rejected: %w", err)
	}
	executable := filepath.Join(extract, m.platform.Executable)
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("SteamCMD archive does not contain the expected executable")
	}
	if m.platform.OS == "linux" {
		if err = os.Chmod(executable, 0700); err != nil {
			return errors.New("SteamCMD executable permissions could not be set")
		}
	}
	if err = os.Rename(extract, m.root); err != nil {
		return errors.New("SteamCMD bootstrap could not be committed")
	}
	if sink != nil {
		sink(Event{"steamcmd_ready", "Managed SteamCMD is ready"})
	}
	return nil
}

func (m *Manager) Install(ctx context.Context, root string, plan InstallPlan, output io.Writer, sink EventSink) error {
	if err := ValidatePlan(plan); err != nil {
		return err
	}
	if !filepath.IsAbs(root) {
		return errors.New("installation root must be absolute")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return errors.New("installation root is unavailable")
	}
	if err = m.Ensure(ctx, sink); err != nil {
		return err
	}
	args := BuildArguments(root, plan)
	if sink != nil {
		sink(Event{"installing", "Installing game files with SteamCMD"})
	}
	environment := map[string]string{}
	if m.platform.OS == "linux" {
		library := filepath.Join(m.root, "linux32")
		if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
			library += string(os.PathListSeparator) + existing
		}
		environment["LD_LIBRARY_PATH"] = library
	}
	command := Command{Executable: m.Executable(), Arguments: args, WorkingDirectory: m.root, Output: output, Environment: environment}
	for attempt := 0; attempt < 2; attempt++ {
		result, runErr := m.runner.Run(ctx, command)
		if ctx.Err() != nil {
			return context.Canceled
		}
		if runErr == nil && result.ExitCode == 0 {
			return nil
		}
		if attempt == 0 && sink != nil {
			sink(Event{"installing", "SteamCMD installation failed transiently; retrying once"})
		}
	}
	return ErrInstallFailed
}

func ValidatePlan(plan InstallPlan) error {
	if plan.AppID <= 0 {
		return errors.New("invalid Steam app id")
	}
	if plan.LoginMode != "anonymous" {
		return errors.New("only anonymous SteamCMD login is supported")
	}
	if plan.BetaBranch != "" && !betaPattern.MatchString(plan.BetaBranch) {
		return errors.New("invalid Steam beta branch")
	}
	return nil
}
func BuildArguments(root string, plan InstallPlan) []string {
	args := []string{"+force_install_dir", root, "+login", "anonymous", "+app_update", strconv.Itoa(plan.AppID)}
	if plan.BetaBranch != "" {
		args = append(args, "-beta", plan.BetaBranch)
	}
	if plan.Validate {
		args = append(args, "validate")
	}
	return append(args, "+quit")
}

type HTTPDownloader struct {
	source string
	client *http.Client
}

func NewHTTPDownloader(source string) *HTTPDownloader { return &HTTPDownloader{source: source} }
func (d *HTTPDownloader) Download(ctx context.Context, destination string) error {
	u, err := url.Parse(d.source)
	if err != nil || (d.source != WindowsURL && d.source != LinuxURL) || u.Scheme != "https" || u.Hostname() != "steamcdn-a.akamaihd.net" {
		return errors.New("untrusted SteamCMD source")
	}
	client := d.client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" || req.URL.Hostname() != "steamcdn-a.akamaihd.net" {
				return errors.New("unsafe redirect")
			}
			return nil
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.source, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("unexpected download status")
	}
	if response.ContentLength > MaxArchiveBytes {
		return errors.New("SteamCMD archive exceeds size limit")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	written, err := io.Copy(file, io.LimitReader(response.Body, MaxArchiveBytes+1))
	if err != nil {
		return err
	}
	if written > MaxArchiveBytes {
		return errors.New("SteamCMD archive exceeds size limit")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	ok = true
	return file.Close()
}

type NativeRunner struct{}

func (NativeRunner) Run(ctx context.Context, command Command) (Result, error) {
	cmd := exec.CommandContext(ctx, command.Executable, command.Arguments...)
	cmd.Dir = command.WorkingDirectory
	cmd.Env = os.Environ()
	for key, value := range command.Environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output := command.Output
	if output == nil {
		output = io.Discard
	}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	result := Result{}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return result, errors.New("SteamCMD process failed")
	}
	return result, nil
}

func sanitized(_ error) error { return errors.New("download unavailable") }

func Extract(kind, archive, destination string) error {
	switch kind {
	case "zip":
		return extractZIP(archive, destination)
	case "tar.gz":
		return extractTarGZ(archive, destination)
	default:
		return errors.New("unsupported SteamCMD archive format")
	}
}
func safeArchivePath(destination, name string) (string, error) {
	normalized := strings.ReplaceAll(name, "\\", "/")
	if normalized == "" || strings.HasPrefix(normalized, "/") || strings.HasPrefix(normalized, "//") || (len(normalized) >= 2 && normalized[1] == ':') {
		return "", errors.New("archive contains an absolute path")
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return "", errors.New("archive contains path traversal")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(normalized))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", errors.New("archive contains path traversal")
	}
	target := filepath.Join(destination, clean)
	relative, err := filepath.Rel(destination, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("archive path escapes destination")
	}
	return target, nil
}
func extractZIP(archive, destination string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return errors.New("invalid zip archive")
	}
	defer reader.Close()
	if len(reader.File) > MaxArchiveEntries {
		return errors.New("archive has too many entries")
	}
	var total int64
	for _, entry := range reader.File {
		target, err := safeArchivePath(destination, entry.Name)
		if err != nil {
			return err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive symlinks are not allowed")
		}
		if entry.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return errors.New("archive special files are not allowed")
		}
		total += int64(entry.UncompressedSize64)
		if total > MaxExtractedBytes {
			return errors.New("extracted archive exceeds size limit")
		}
		if err = writeZIPEntry(entry, target); err != nil {
			return err
		}
	}
	return nil
}
func writeZIPEntry(entry *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer destination.Close()
	written, err := io.Copy(destination, io.LimitReader(source, int64(entry.UncompressedSize64)+1))
	if err != nil || written != int64(entry.UncompressedSize64) {
		return errors.New("zip entry size mismatch")
	}
	return nil
}
func extractTarGZ(archive, destination string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return errors.New("invalid gzip archive")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	var total int64
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("invalid tar archive")
		}
		entries++
		if entries > MaxArchiveEntries {
			return errors.New("archive has too many entries")
		}
		target, err := safeArchivePath(destination, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				return errors.New("invalid archive entry size")
			}
			total += header.Size
			if total > MaxExtractedBytes {
				return errors.New("extracted archive exceeds size limit")
			}
			if err = writeTarEntry(reader, target, header.Size); err != nil {
				return err
			}
		default:
			return errors.New("archive links and special files are not allowed")
		}
	}
	return nil
}
func writeTarEntry(reader io.Reader, target string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	written, err := io.Copy(file, io.LimitReader(reader, size+1))
	if err != nil || written != size {
		return errors.New("tar entry size mismatch")
	}
	return nil
}
