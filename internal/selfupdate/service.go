// Package selfupdate lets a GameNode installation update its own binary from
// this project's published GitHub releases.
//
// The design is deliberately narrow and defensive (see
// docs/adr/0013-self-update.md):
//
//   - The source is fixed in code. Download URLs are built from a validated
//     release tag and fixed asset names, never taken from an API response,
//     a caller, or a remote controller.
//   - A binary is only installed after its SHA-256 matches the release's own
//     SHA256SUMS.txt, it looks like an executable for this platform, and it
//     reports the expected version when run with --version.
//   - Installing is a separate, explicit step that re-runs every safety check,
//     backs up the database, and keeps the previous binary for rollback.
//   - A new binary must prove itself healthy; otherwise the previous binary is
//     restored automatically on the next boots.
//
// The package has no dependency on HTTP, RBAC, servers, provisioning, or the
// database driver: those facts reach it through Options callbacks composed in
// cmd/gamenode.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type State string

const (
	StateIdle        State = "idle"
	StateChecking    State = "checking"
	StateDownloading State = "downloading"
	StateReady       State = "ready" // a verified binary is staged
	StateApplying    State = "applying"
	StateRestarting  State = "restarting"
	StateFailed      State = "failed"
)

type CheckStatus string

const (
	CheckPass  CheckStatus = "pass"
	CheckWarn  CheckStatus = "warn"  // allowed only with explicit acknowledgement
	CheckBlock CheckStatus = "block" // never allowed
)

// Check is one named safety check. Message is controlled, non-sensitive text.
type Check struct {
	ID      string      `json:"id"`
	Label   string      `json:"label"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message"`
}

type Progress struct {
	DownloadedBytes int64 `json:"downloaded_bytes"`
	TotalBytes      int64 `json:"total_bytes"`
}

type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Staged struct {
	Version    string    `json:"version"`
	SHA256     string    `json:"sha256"`
	VerifiedAt time.Time `json:"verified_at"`
}

// Outcome is the recorded result of an earlier update attempt, reported once
// (for audit) by the process that observes it.
type Outcome struct {
	Result        string    `json:"result"` // "completed" or "rolled_back"
	From          string    `json:"from"`
	To            string    `json:"to"`
	At            time.Time `json:"at"`
	Reason        string    `json:"reason,omitempty"`
	ActorID       string    `json:"-"`
	ActorUsername string    `json:"-"`
}

// Status is the API-facing snapshot. It contains no host paths.
type Status struct {
	CurrentVersion   string     `json:"current_version"`
	OS               string     `json:"os"`
	Arch             string     `json:"arch"`
	Updatable        bool       `json:"updatable"`
	UpdatableReason  string     `json:"updatable_reason,omitempty"`
	State            State      `json:"state"`
	Progress         *Progress  `json:"progress,omitempty"`
	UpdateAvailable  bool       `json:"update_available"`
	Available        *Release   `json:"available,omitempty"`
	LastCheckedAt    *time.Time `json:"last_checked_at,omitempty"`
	LastCheckError   *ErrorInfo `json:"last_check_error,omitempty"`
	Staged           *Staged    `json:"staged,omitempty"`
	Error            *ErrorInfo `json:"error,omitempty"`
	Checks           []Check    `json:"checks"`
	CanPrepare       bool       `json:"can_prepare"`
	CanApply         bool       `json:"can_apply"`
	RequiresAck      bool       `json:"requires_acknowledgement"`
	RestartMode      string     `json:"restart_mode"`
	LastOutcome      *Outcome   `json:"last_outcome,omitempty"`
	MinCheckInterval int        `json:"min_check_interval_seconds"`
}

// Error codes returned by Service operations. The API layer maps these to
// HTTP statuses; none carries raw external output.
const (
	CodeBusy               = "update_busy"
	CodeReleaseUnknown     = "release_unknown"
	CodeNotReady           = "update_not_ready"
	CodePreflightBlocked   = "preflight_blocked"
	CodeAcknowledge        = "acknowledgement_required"
	CodeUnsupported        = "update_unsupported"
	CodeIncompleteRelease  = "release_incomplete"
	CodeChecksumMismatch   = "checksum_mismatch"
	CodeChecksumUnavail    = "checksum_unavailable"
	CodeSizeMismatch       = "size_mismatch"
	CodeNotExecutable      = "not_executable"
	CodeSelfTestFailed     = "self_test_failed"
	CodeDownloadFailed     = "download_failed"
	CodeBackupFailed       = "database_backup_failed"
	CodeInstallFailed      = "install_failed"
	CodeMarkerFailed       = "rollback_marker_failed"
	CodeStagedTampered     = "staged_binary_changed"
	CodeDownloadCancelled  = "download_cancelled"
	restartModeSelf        = "self"
	restartModeExit        = "exit"
	defaultConfirmAfter    = 60 * time.Second
	defaultRestartDelay    = 1500 * time.Millisecond
	downloadTimeout        = 20 * time.Minute
	minManualCheckInterval = 15 * time.Second
	checkCacheTTL          = 3 * time.Second
	spaceMargin            = 64 << 20
	backupsKept            = 3
	// maxUnconfirmedBoots is how many starts of a freshly installed binary are
	// tolerated without it ever reaching the health confirmation. The third
	// unconfirmed start restores the previous binary instead of running.
	maxUnconfirmedBoots = 2
)

// Error is the single error type Service operations return.
type Error struct {
	Code    string
	Message string
	Checks  []Check
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newError(code, message string) *Error { return &Error{Code: code, Message: message} }

// Actor identifies who requested an install, recorded only so the completion
// (or rollback) audit event after the restart can name them.
type Actor struct {
	ID       string
	Username string
}

// Activity is the point-in-time workload summary preflight uses.
type Activity struct {
	RunningServers      int
	TransitionalServers int
	ProvisioningJobs    int
	ServerUpdateJobs    int
}

// Options wires the updater to the rest of the process.
type Options struct {
	DataDirectory  string
	DatabasePath   string
	CurrentVersion string
	Source         Source
	// RestartMode is "self" (default: replace/relaunch the process) or "exit"
	// (exit and let a supervisor restart it).
	RestartMode string
	// Args are the command-line arguments (without argv[0]) the relaunched
	// process receives.
	Args []string

	Activity       func(context.Context) (Activity, error)
	DatabaseCheck  func(context.Context) error
	DatabaseBackup func(ctx context.Context, path string) error
	Log            *slog.Logger

	// The fields below exist for tests.
	Executable   string
	GOOS, GOARCH string
	Now          func() time.Time
	ConfirmAfter time.Duration
	RestartDelay time.Duration
	FreeBytes    func(dir string) (uint64, error)
	SelfTest     func(ctx context.Context, path string) (string, error)
}

type Service struct {
	opts      Options
	exe       string
	goos      string
	goarch    string
	assetName string // resolved release asset name, "" when unsupported
	now       func() time.Time
	log       *slog.Logger

	mu            sync.Mutex
	state         State
	available     *Release
	lastChecked   time.Time
	lastCheckErr  *ErrorInfo
	staged        *Staged
	lastError     *ErrorInfo
	lastOutcome   *Outcome
	pendingReport *Outcome
	cancel        context.CancelFunc
	downloaded    atomic.Int64
	total         atomic.Int64
	checkCache    []Check
	checkCachePh  string
	checkCacheAt  time.Time

	restartOnce sync.Once
	restartCh   chan struct{}
	restartFlag atomic.Bool
}

func New(opts Options) (*Service, error) {
	if opts.Source == nil {
		return nil, errors.New("selfupdate: source is required")
	}
	if opts.DataDirectory == "" {
		return nil, errors.New("selfupdate: data directory is required")
	}
	s := &Service{opts: opts, state: StateIdle, restartCh: make(chan struct{})}
	s.goos, s.goarch = opts.GOOS, opts.GOARCH
	if s.goos == "" {
		s.goos = runtime.GOOS
	}
	if s.goarch == "" {
		s.goarch = runtime.GOARCH
	}
	s.assetName, _ = AssetName(s.goos, s.goarch)
	s.now = opts.Now
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	s.log = opts.Log
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.opts.ConfirmAfter <= 0 {
		s.opts.ConfirmAfter = defaultConfirmAfter
	}
	if s.opts.RestartDelay <= 0 {
		s.opts.RestartDelay = defaultRestartDelay
	}
	if s.opts.RestartMode != restartModeExit {
		s.opts.RestartMode = restartModeSelf
	}
	if s.opts.FreeBytes == nil {
		s.opts.FreeBytes = freeBytes
	}
	if s.opts.SelfTest == nil {
		s.opts.SelfTest = runVersionSelfTest
	}
	// The executable path is captured once, before any swap: after a rename,
	// os.Executable on Linux would report the backup name.
	exe := opts.Executable
	if exe == "" {
		resolved, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("selfupdate: resolve executable: %w", err)
		}
		exe = resolved
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	s.exe = exe
	return s, nil
}

// RestartMode reports how a restart is performed.
func (s *Service) RestartMode() string { return s.opts.RestartMode }

// RestartRequested is closed once an installed update needs the process to
// restart. cmd/gamenode reacts by shutting down cleanly.
func (s *Service) RestartRequested() <-chan struct{} { return s.restartCh }

// RestartPending reports whether run() should hand off to Relaunch after all
// deferred cleanup has completed.
func (s *Service) RestartPending() bool { return s.restartFlag.Load() }

func (s *Service) requestRestart() {
	s.restartOnce.Do(func() {
		s.restartFlag.Store(true)
		close(s.restartCh)
	})
}

// Relaunch hands the process over to the (new or restored) binary. It must be
// called only after the database, listeners, and log files are released. In
// "exit" mode it does nothing: the caller simply returns and a supervisor
// restarts the process.
func (s *Service) Relaunch() error {
	if s.opts.RestartMode == restartModeExit {
		return nil
	}
	return relaunch(s.exe, s.opts.Args)
}

func (s *Service) updatable() (bool, string) {
	if s.assetName == "" {
		return false, "Self-update is not available for " + s.goos + "/" + s.goarch + " builds."
	}
	if _, err := ParseVersion(s.opts.CurrentVersion); err != nil {
		return false, "This is a development or unversioned build; only official release builds can update themselves."
	}
	return true, ""
}

// Status returns a snapshot and freshly evaluates the safety checks (cached
// for a few seconds so a polling UI does not hammer the server list).
func (s *Service) Status(ctx context.Context) Status {
	s.mu.Lock()
	out := s.snapshotLocked()
	phase := phasePrepare
	if s.state == StateReady {
		phase = phaseApply
	}
	release := s.available
	stagedNow := s.staged
	state := s.state
	s.mu.Unlock()

	// Checks are meaningful only while an operator can still act on them; not
	// mid-download or mid-install, when they would only be noise.
	settled := state == StateIdle || state == StateFailed || state == StateReady
	if settled && (out.UpdateAvailable || state != StateIdle) {
		out.Checks = s.cachedChecks(ctx, phase, release, stagedNow)
	} else {
		out.Checks = []Check{}
	}
	blocked, warned := summarize(out.Checks)
	out.RequiresAck = warned
	out.CanPrepare = out.Updatable && out.UpdateAvailable && !blocked && (state == StateIdle || state == StateFailed || state == StateReady)
	out.CanApply = state == StateReady && !blocked
	return out
}

func (s *Service) snapshotLocked() Status {
	updatable, reason := s.updatable()
	out := Status{
		CurrentVersion: s.opts.CurrentVersion, OS: s.goos, Arch: s.goarch,
		Updatable: updatable, UpdatableReason: reason, State: s.state,
		RestartMode: s.opts.RestartMode, MinCheckInterval: int(minManualCheckInterval / time.Second),
		Checks: []Check{},
	}
	if s.state == StateDownloading {
		out.Progress = &Progress{DownloadedBytes: s.downloaded.Load(), TotalBytes: s.total.Load()}
	}
	if s.available != nil {
		release := *s.available
		out.Available = &release
		if current, err := ParseVersion(s.opts.CurrentVersion); err == nil {
			if target, err := ParseVersion(release.Version); err == nil && Compare(target, current) > 0 {
				out.UpdateAvailable = true
			}
		}
	}
	if !s.lastChecked.IsZero() {
		checked := s.lastChecked
		out.LastCheckedAt = &checked
	}
	out.LastCheckError = s.lastCheckErr
	if s.staged != nil {
		staged := *s.staged
		out.Staged = &staged
	}
	out.Error = s.lastError
	if s.lastOutcome != nil {
		outcome := *s.lastOutcome
		out.LastOutcome = &outcome
	}
	return out
}

func summarize(checks []Check) (blocked, warned bool) {
	for _, check := range checks {
		switch check.Status {
		case CheckBlock:
			blocked = true
		case CheckWarn:
			warned = true
		}
	}
	return
}

func (s *Service) cachedChecks(ctx context.Context, phase string, release *Release, staged *Staged) []Check {
	s.mu.Lock()
	if s.checkCache != nil && s.checkCachePh == phase && s.now().Sub(s.checkCacheAt) < checkCacheTTL {
		cached := append([]Check(nil), s.checkCache...)
		s.mu.Unlock()
		return cached
	}
	s.mu.Unlock()
	checks := s.evaluate(ctx, phase, release, staged)
	s.mu.Lock()
	s.checkCache, s.checkCachePh, s.checkCacheAt = append([]Check(nil), checks...), phase, s.now()
	s.mu.Unlock()
	return checks
}

func (s *Service) invalidateCacheLocked() { s.checkCache = nil }

// Check asks the release source for the newest published release. Repeated
// manual checks inside minManualCheckInterval return the cached result.
func (s *Service) Check(ctx context.Context) (Status, error) {
	s.mu.Lock()
	if s.state == StateChecking {
		s.mu.Unlock()
		return s.Status(ctx), nil
	}
	if s.lastCheckErr == nil && !s.lastChecked.IsZero() && s.now().Sub(s.lastChecked) < minManualCheckInterval {
		s.mu.Unlock()
		return s.Status(ctx), nil
	}
	previous := s.state
	transient := previous == StateIdle || previous == StateFailed
	if transient {
		s.state = StateChecking
	}
	s.mu.Unlock()

	release, err := s.opts.Source.Latest(ctx)

	s.mu.Lock()
	if transient {
		s.state = previous
	}
	s.lastChecked = s.now()
	s.invalidateCacheLocked()
	if err != nil {
		var sourceErr *SourceError
		if errors.As(err, &sourceErr) {
			s.lastCheckErr = &ErrorInfo{Code: sourceErr.Code, Message: sourceErr.Message}
		} else {
			s.lastCheckErr = &ErrorInfo{Code: errUnavailable.Code, Message: errUnavailable.Message}
		}
		// A "no release" answer is a legitimate state, not a failure to hide.
		if sourceErr != nil && sourceErr.Code == errNoRelease.Code {
			s.available = nil
		}
		s.mu.Unlock()
		s.log.Warn("update check failed", "module", "SelfUpdate.Check", "code", s.lastCheckErr.Code)
		return s.Status(ctx), nil
	}
	s.lastCheckErr = nil
	// A different release invalidates anything staged for the previous one.
	if s.available != nil && s.available.Version != release.Version && s.staged != nil && s.state == StateReady {
		s.discardStagedLocked()
	}
	copyRelease := release
	s.available = &copyRelease
	s.mu.Unlock()
	s.log.Info("update check completed", "module", "SelfUpdate.Check", "latest", release.Version, "current", s.opts.CurrentVersion)
	return s.Status(ctx), nil
}

func (s *Service) discardStagedLocked() {
	_ = os.Remove(StagedPath(s.exe))
	s.staged = nil
	if s.state == StateReady {
		s.state = StateIdle
	}
	s.invalidateCacheLocked()
}

const (
	phasePrepare = "prepare"
	phaseApply   = "apply"
)

// evaluate runs the safety checks. In the prepare phase the artifact does not
// exist yet, so artifact checks are omitted; in the apply phase everything
// runs. A blocking check can never be overridden by the caller.
func (s *Service) evaluate(ctx context.Context, phase string, release *Release, staged *Staged) []Check {
	activityFn, databaseCheckFn, _ := s.bound()
	var checks []Check
	add := func(id, label string, status CheckStatus, message string) {
		checks = append(checks, Check{ID: id, Label: label, Status: status, Message: message})
	}

	if s.assetName == "" {
		add("platform", "Supported platform", CheckBlock, "No official release binary exists for "+s.goos+"/"+s.goarch+".")
	} else {
		add("platform", "Supported platform", CheckPass, "Release binary "+s.assetName+" matches this host.")
	}

	current, currentErr := ParseVersion(s.opts.CurrentVersion)
	if currentErr != nil {
		add("release_build", "Official release build", CheckBlock, "This is a development or unversioned build; only official release builds can update themselves.")
	} else {
		add("release_build", "Official release build", CheckPass, "Running version "+current.String()+".")
	}

	if release == nil {
		add("release", "Release selected", CheckBlock, "No release has been selected. Check for updates first.")
	} else if target, err := ParseVersion(release.Version); err != nil {
		add("release", "Release selected", CheckBlock, "The release version is not a valid semantic version.")
	} else if currentErr == nil && Compare(target, current) <= 0 {
		add("newer_version", "Newer than installed", CheckBlock, "Release "+target.String()+" is not newer than the installed version; downgrades and reinstalls are not performed.")
	} else if currentErr == nil {
		add("newer_version", "Newer than installed", CheckPass, "Release "+target.String()+" is newer than "+current.String()+".")
	}

	if release != nil && s.assetName != "" {
		_, hasBinary := release.Asset(s.assetName)
		_, hasSums := release.Asset(ChecksumAsset)
		switch {
		case !hasBinary || !hasSums:
			add("release_assets", "Release is complete", CheckBlock, "The release does not publish both the "+s.assetName+" binary and "+ChecksumAsset+".")
		default:
			add("release_assets", "Release is complete", CheckPass, "Binary and checksum manifest are published.")
		}
	}

	dir := filepath.Dir(s.exe)
	if err := probeWritable(dir); err != nil {
		add("executable_writable", "Installation directory writable", CheckBlock, "GameNode cannot replace its own executable: the installation directory is not writable by this process (read-only install, container image, or insufficient permissions).")
	} else {
		add("executable_writable", "Installation directory writable", CheckPass, "The executable can be replaced in place.")
	}

	s.evaluateDiskSpace(phase, release, add)

	if activityFn == nil {
		add("activity", "Workload activity", CheckBlock, "Workload activity cannot be determined.")
	} else if activity, err := activityFn(ctx); err != nil {
		add("activity", "Workload activity", CheckBlock, "Workload activity could not be determined.")
	} else {
		if activity.ProvisioningJobs+activity.ServerUpdateJobs > 0 {
			add("active_jobs", "No provisioning or update jobs running", CheckBlock, fmt.Sprintf("%d provisioning or server-update job(s) are in progress; restarting would interrupt them.", activity.ProvisioningJobs+activity.ServerUpdateJobs))
		} else {
			add("active_jobs", "No provisioning or update jobs running", CheckPass, "No provisioning or server-update jobs are running.")
		}
		if activity.TransitionalServers > 0 {
			add("server_transitions", "No servers starting or stopping", CheckBlock, fmt.Sprintf("%d server(s) are starting or stopping; wait until they settle.", activity.TransitionalServers))
		} else {
			add("server_transitions", "No servers starting or stopping", CheckPass, "All servers are in a settled state.")
		}
		if activity.RunningServers > 0 {
			add("running_servers", "Running servers", CheckWarn, fmt.Sprintf("%d server(s) are running. They keep running through the restart, but live consoles of native servers detach and cannot be reattached. Brief management downtime is expected.", activity.RunningServers))
		} else {
			add("running_servers", "Running servers", CheckPass, "No servers are running.")
		}
	}

	if databaseCheckFn == nil {
		add("database", "Database healthy", CheckBlock, "Database health cannot be determined.")
	} else if err := databaseCheckFn(ctx); err != nil {
		add("database", "Database healthy", CheckBlock, "The database did not pass its integrity check; refusing to update.")
	} else {
		add("database", "Database healthy", CheckPass, "The database passed its integrity check and will be backed up before the update.")
	}

	if s.opts.RestartMode == restartModeExit {
		add("restart_mode", "Restart mechanism", CheckWarn, "GameNode is configured to exit after installing (update.restart_mode: exit). A supervisor such as systemd with Restart=always must start it again.")
	} else {
		add("restart_mode", "Restart mechanism", CheckPass, "GameNode will restart itself after installing.")
	}

	if phase == phaseApply {
		if staged == nil {
			add("artifact", "Verified download", CheckBlock, "No verified binary is staged.")
		} else if release != nil && staged.Version != release.Version {
			add("artifact", "Verified download", CheckBlock, "The staged binary belongs to a different release; download it again.")
		} else if _, err := os.Stat(StagedPath(s.exe)); err != nil {
			add("artifact", "Verified download", CheckBlock, "The staged binary is missing; download it again.")
		} else {
			add("artifact", "Verified download", CheckPass, "SHA-256 matched the release manifest and the binary passed its self-test.")
		}
	}
	return checks
}

func (s *Service) evaluateDiskSpace(phase string, release *Release, add func(id, label string, status CheckStatus, message string)) {
	const id, label = "disk_space", "Free disk space"
	var binarySize int64
	if release != nil && s.assetName != "" {
		if asset, ok := release.Asset(s.assetName); ok {
			binarySize = asset.Size
		}
	}
	exeNeed := uint64(spaceMargin)
	if phase == phasePrepare {
		exeNeed += uint64(binarySize)
	}
	dbNeed := uint64(spaceMargin)
	if s.opts.DatabasePath != "" {
		for _, suffix := range []string{"", "-wal"} {
			if info, err := os.Stat(s.opts.DatabasePath + suffix); err == nil {
				dbNeed += uint64(info.Size())
			}
		}
	}
	exeDir, dataDir := filepath.Dir(s.exe), s.opts.DataDirectory
	// Both directories on one volume share the same free space.
	exeFree, exeErr := s.opts.FreeBytes(exeDir)
	dataFree, dataErr := s.opts.FreeBytes(dataDir)
	if exeErr != nil || dataErr != nil {
		add(id, label, CheckWarn, "Free disk space could not be measured on this platform; make sure the installation and data volumes have room for the download and a database backup.")
		return
	}
	if sameVolume(exeDir, dataDir) {
		if exeFree < exeNeed+dbNeed {
			add(id, label, CheckBlock, fmt.Sprintf("At least %s of free space is required for the download and database backup; %s is available.", humanBytes(exeNeed+dbNeed), humanBytes(exeFree)))
			return
		}
	} else if exeFree < exeNeed {
		add(id, label, CheckBlock, fmt.Sprintf("At least %s of free space is required on the installation volume; %s is available.", humanBytes(exeNeed), humanBytes(exeFree)))
		return
	} else if dataFree < dbNeed {
		add(id, label, CheckBlock, fmt.Sprintf("At least %s of free space is required on the data volume for the database backup; %s is available.", humanBytes(dbNeed), humanBytes(dataFree)))
		return
	}
	add(id, label, CheckPass, "Enough free space for the download and a database backup.")
}

func sameVolume(a, b string) bool {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b))
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for value := n / unit; value >= unit; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func probeWritable(dir string) error {
	file, err := os.CreateTemp(dir, ".gamenode-write-probe-*")
	if err != nil {
		return err
	}
	name := file.Name()
	_ = file.Close()
	return os.Remove(name)
}

func blockingChecks(checks []Check) []Check {
	var out []Check
	for _, check := range checks {
		if check.Status == CheckBlock {
			out = append(out, check)
		}
	}
	return out
}

func sameVersion(a, b string) bool {
	va, errA := ParseVersion(a)
	vb, errB := ParseVersion(b)
	return errA == nil && errB == nil && Compare(va, vb) == 0
}

// Prepare downloads and verifies the release binary in the background. It
// does not install anything.
func (s *Service) Prepare(ctx context.Context, version string) error {
	s.mu.Lock()
	switch s.state {
	case StateDownloading, StateApplying, StateRestarting, StateChecking:
		s.mu.Unlock()
		return newError(CodeBusy, "another update operation is in progress")
	}
	if s.available == nil || !sameVersion(s.available.Version, version) {
		s.mu.Unlock()
		return newError(CodeReleaseUnknown, "the requested version is not the latest release; check for updates again")
	}
	release := *s.available
	s.mu.Unlock()

	checks := s.evaluate(ctx, phasePrepare, &release, nil)
	if blocked := blockingChecks(checks); len(blocked) > 0 {
		return &Error{Code: CodePreflightBlocked, Message: "safety checks are blocking this update", Checks: checks}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case StateDownloading, StateApplying, StateRestarting, StateChecking:
		return newError(CodeBusy, "another update operation is in progress")
	}
	if s.staged != nil {
		s.discardStagedLocked()
	}
	runCtx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	s.cancel = cancel
	s.state = StateDownloading
	s.lastError = nil
	s.downloaded.Store(0)
	s.total.Store(0)
	s.invalidateCacheLocked()
	go s.download(runCtx, cancel, release)
	return nil
}

// Cancel aborts a running download or discards a staged, not-yet-installed
// binary. It never affects an install already in progress.
func (s *Service) Cancel() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case StateDownloading:
		if s.cancel != nil {
			s.cancel()
		}
		return nil
	case StateReady:
		s.discardStagedLocked()
		return nil
	case StateApplying, StateRestarting:
		return newError(CodeBusy, "the update is already being installed and can no longer be cancelled")
	}
	return nil
}

func (s *Service) download(ctx context.Context, cancel context.CancelFunc, release Release) {
	defer cancel()
	staged, err := s.fetchAndVerify(ctx, release)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = nil
	s.invalidateCacheLocked()
	if err != nil {
		_ = os.Remove(StagedPath(s.exe))
		s.staged = nil
		var updateErr *Error
		switch {
		case errors.As(err, &updateErr):
			if updateErr.Code == CodeDownloadCancelled {
				s.state = StateIdle
				return
			}
			s.lastError = &ErrorInfo{Code: updateErr.Code, Message: updateErr.Message}
		default:
			s.lastError = &ErrorInfo{Code: CodeDownloadFailed, Message: "the release could not be downloaded"}
		}
		s.state = StateFailed
		s.log.Warn("update download failed", "module", "SelfUpdate.Prepare", "code", s.lastError.Code)
		return
	}
	s.staged = &staged
	s.state = StateReady
	s.log.Info("update downloaded and verified", "module", "SelfUpdate.Prepare", "version", staged.Version)
}

func (s *Service) fetchAndVerify(ctx context.Context, release Release) (Staged, error) {
	binaryAsset, ok := release.Asset(s.assetName)
	if !ok {
		return Staged{}, newError(CodeIncompleteRelease, "the release does not publish a binary for this platform")
	}
	// Integrity first: fetch the manifest and resolve the expected digest
	// before writing a single byte of the binary to disk.
	manifestReader, _, err := s.opts.Source.Open(ctx, release, ChecksumAsset, MaxChecksumBytes)
	if err != nil {
		return Staged{}, s.sourceFailure(ctx, err, CodeChecksumUnavail, "the release checksum manifest could not be downloaded")
	}
	manifest, err := io.ReadAll(io.LimitReader(manifestReader, MaxChecksumBytes+1))
	_ = manifestReader.Close()
	if err != nil || int64(len(manifest)) > MaxChecksumBytes {
		return Staged{}, newError(CodeChecksumUnavail, "the release checksum manifest could not be read")
	}
	expected, err := ParseChecksum(manifest, s.assetName)
	if err != nil {
		return Staged{}, newError(CodeChecksumUnavail, "the release checksum manifest has no valid entry for this platform")
	}

	body, contentLength, err := s.opts.Source.Open(ctx, release, s.assetName, MaxBinaryBytes)
	if err != nil {
		return Staged{}, s.sourceFailure(ctx, err, CodeDownloadFailed, "the release binary could not be downloaded")
	}
	defer body.Close()
	total := binaryAsset.Size
	if total <= 0 {
		total = contentLength
	}
	s.total.Store(total)

	path := StagedPath(s.exe)
	_ = os.Remove(path)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return Staged{}, newError(CodeDownloadFailed, "the download could not be written next to the executable")
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher, progressWriter{&s.downloaded}), body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return Staged{}, s.sourceFailure(ctx, copyErr, CodeDownloadFailed, "the release binary download did not complete")
	}
	if syncErr != nil || closeErr != nil {
		return Staged{}, newError(CodeDownloadFailed, "the download could not be flushed to disk")
	}
	if binaryAsset.Size > 0 && written != binaryAsset.Size {
		return Staged{}, newError(CodeSizeMismatch, "the downloaded size does not match the size published for the release")
	}
	var actual [32]byte
	copy(actual[:], hasher.Sum(nil))
	if actual != expected {
		return Staged{}, newError(CodeChecksumMismatch, "the SHA-256 of the download does not match the release checksum manifest")
	}
	if err := validateExecutable(path, s.goos); err != nil {
		return Staged{}, newError(CodeNotExecutable, "the downloaded file is not a valid executable for this platform")
	}
	reported, err := s.opts.SelfTest(ctx, path)
	if err != nil || !sameVersion(reported, release.Version) {
		return Staged{}, newError(CodeSelfTestFailed, "the downloaded binary failed its self-test (it did not start, or reported a different version) and was discarded")
	}
	return Staged{Version: release.Version, SHA256: hex.EncodeToString(actual[:]), VerifiedAt: s.now()}, nil
}

func (s *Service) sourceFailure(ctx context.Context, err error, code, message string) error {
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return newError(CodeDownloadCancelled, "download cancelled")
	}
	var sourceErr *SourceError
	if errors.As(err, &sourceErr) {
		return newError(code, sourceErr.Message)
	}
	return newError(code, message)
}

type progressWriter struct{ counter *atomic.Int64 }

func (p progressWriter) Write(b []byte) (int, error) {
	p.counter.Add(int64(len(b)))
	return len(b), nil
}

// Apply installs the staged, verified binary and schedules the restart. Every
// safety check runs again immediately before the swap, so a state change since
// the administrator last looked cannot slip through. Warnings require
// acknowledge; blocks never can be overridden.
func (s *Service) Apply(ctx context.Context, version string, acknowledge bool, actor Actor) error {
	s.mu.Lock()
	switch s.state {
	case StateApplying, StateRestarting, StateDownloading:
		s.mu.Unlock()
		return newError(CodeBusy, "another update operation is in progress")
	}
	if s.state != StateReady || s.staged == nil || s.available == nil {
		s.mu.Unlock()
		return newError(CodeNotReady, "no verified update is staged; download the update first")
	}
	if !sameVersion(s.staged.Version, version) || !sameVersion(s.available.Version, version) {
		s.mu.Unlock()
		return newError(CodeReleaseUnknown, "the requested version does not match the staged update")
	}
	release, staged := *s.available, *s.staged
	// Claim the operation before doing slow work, without holding the lock.
	s.state = StateApplying
	s.invalidateCacheLocked()
	s.mu.Unlock()

	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()

	fail := func(err *Error, keepStaged bool) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.invalidateCacheLocked()
		if keepStaged {
			s.state = StateReady
		} else {
			_ = os.Remove(StagedPath(s.exe))
			s.staged = nil
			s.state = StateFailed
			s.lastError = &ErrorInfo{Code: err.Code, Message: err.Message}
		}
		return err
	}

	checks := s.evaluate(opCtx, phaseApply, &release, &staged)
	if len(blockingChecks(checks)) > 0 {
		return fail(&Error{Code: CodePreflightBlocked, Message: "safety checks are blocking this update", Checks: checks}, true)
	}
	if _, warned := summarize(checks); warned && !acknowledge {
		return fail(&Error{Code: CodeAcknowledge, Message: "the update has warnings that must be acknowledged", Checks: checks}, true)
	}

	// The staged file sat on disk between "download" and "install". Re-hash it
	// so a replaced or truncated file can never be installed.
	if digest, err := hashFile(StagedPath(s.exe)); err != nil || hex.EncodeToString(digest[:]) != staged.SHA256 {
		return fail(newError(CodeStagedTampered, "the staged binary changed after verification and was discarded"), false)
	}

	backupPath := ""
	_, _, databaseBackupFn := s.bound()
	if databaseBackupFn != nil {
		dir := filepath.Join(updatesDir(s.opts.DataDirectory), "backups")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(newError(CodeBackupFailed, "the database backup directory could not be created"), true)
		}
		backupPath = filepath.Join(dir, fmt.Sprintf("pre-update-%s-to-%s-%s.db", safeName(CanonicalVersion(s.opts.CurrentVersion)), safeName(release.Version), s.now().Format("20060102T150405Z")))
		if err := databaseBackupFn(opCtx, backupPath); err != nil {
			_ = os.Remove(backupPath)
			return fail(newError(CodeBackupFailed, "the database could not be backed up; the update was not installed"), true)
		}
		pruneBackups(dir, backupsKept)
	}

	backupExe, err := replaceExecutable(s.exe, StagedPath(s.exe))
	if err != nil {
		s.log.Error("update install failed", "module", "SelfUpdate.Apply", "error", err.Error())
		return fail(newError(CodeInstallFailed, "the new executable could not be put in place; the current version is unchanged"), true)
	}
	m := marker{State: markerApplied, From: CanonicalVersion(s.opts.CurrentVersion), To: release.Version, AppliedAt: s.now(), ActorID: actor.ID, ActorUsername: actor.Username}
	if err := writeMarker(s.opts.DataDirectory, m); err != nil {
		// Without the rollback marker an unhealthy update could not be undone,
		// so undo the swap instead of proceeding.
		if restoreErr := restoreBackup(s.exe, backupExe); restoreErr != nil {
			s.log.Error("could not undo update after marker failure", "module", "SelfUpdate.Apply", "error", restoreErr.Error())
		}
		return fail(newError(CodeMarkerFailed, "the rollback record could not be written; the update was not installed"), false)
	}

	s.mu.Lock()
	s.staged = nil
	s.state = StateRestarting
	s.invalidateCacheLocked()
	s.mu.Unlock()
	s.log.Info("update installed; restart scheduled", "module", "SelfUpdate.Apply", "from", s.opts.CurrentVersion, "to", release.Version, "restart_mode", s.opts.RestartMode)
	time.AfterFunc(s.opts.RestartDelay, s.requestRestart)
	return nil
}

func hashFile(path string) ([32]byte, error) {
	var out [32]byte
	file, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return out, err
	}
	copy(out[:], hasher.Sum(nil))
	return out, nil
}

func safeName(version string) string {
	var b strings.Builder
	for _, r := range version {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func pruneBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type backup struct {
		name    string
		modTime time.Time
	}
	var backups []backup
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "pre-update-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backup{entry.Name(), info.ModTime()})
	}
	// Oldest first by modification time. The file name starts with the version
	// being left ("1.10.0" sorts before "1.9.0" as text), so it cannot order
	// backups chronologically; it only breaks exact time ties.
	sort.Slice(backups, func(i, j int) bool {
		if !backups[i].modTime.Equal(backups[j].modTime) {
			return backups[i].modTime.Before(backups[j].modTime)
		}
		return backups[i].name < backups[j].name
	})
	for len(backups) > keep {
		_ = os.Remove(filepath.Join(dir, backups[0].name))
		backups = backups[1:]
	}
}

// BootResult tells run() what to do after the startup health bookkeeping.
type BootResult struct {
	// Relaunch is true when the previous binary was just restored and the
	// process must hand over to it immediately.
	Relaunch bool
}

// Boot performs the startup half of the update lifecycle. Call it once, early,
// before the database is opened.
//
//   - Leftover staging files from an interrupted download are removed (their
//     verification state lived only in memory).
//   - If this process is a freshly installed binary that has not yet proven
//     itself, the attempt is counted; after maxUnconfirmedBoots unconfirmed
//     starts the previous binary is restored instead of running this one.
func (s *Service) Boot() BootResult {
	_ = os.Remove(StagedPath(s.exe))
	m, present, err := readMarker(s.opts.DataDirectory)
	if err != nil {
		s.log.Warn("update marker unreadable; discarding it", "module", "SelfUpdate.Boot", "error", err.Error())
		_ = removeMarker(s.opts.DataDirectory)
		return BootResult{}
	}
	if !present {
		return BootResult{}
	}
	running := s.opts.CurrentVersion
	switch m.State {
	case markerApplied:
		if !sameVersion(running, m.To) {
			// The swap did not take effect or was undone manually.
			_ = removeMarker(s.opts.DataDirectory)
			return BootResult{}
		}
		m.BootAttempts++
		if m.BootAttempts > maxUnconfirmedBoots {
			s.log.Error("updated binary never became healthy; restoring the previous version", "module", "SelfUpdate.Boot", "from", m.From, "to", m.To, "attempts", m.BootAttempts)
			if err := restoreBackup(s.exe, BackupPath(s.exe)); err != nil {
				s.log.Error("rollback failed; continuing with the updated binary", "module", "SelfUpdate.Boot", "error", err.Error())
				_ = removeMarker(s.opts.DataDirectory)
				return BootResult{}
			}
			m.State, m.Reason = markerRolledBack, "unhealthy_after_update"
			if err := writeMarker(s.opts.DataDirectory, m); err != nil {
				s.log.Warn("could not record rollback", "module", "SelfUpdate.Boot", "error", err.Error())
			}
			return BootResult{Relaunch: true}
		}
		if err := writeMarker(s.opts.DataDirectory, m); err != nil {
			s.log.Warn("could not record boot attempt", "module", "SelfUpdate.Boot", "error", err.Error())
		}
	case markerRolledBack:
		if sameVersion(running, m.From) {
			s.mu.Lock()
			s.pendingReport = &Outcome{Result: "rolled_back", From: m.From, To: m.To, At: s.now(), Reason: m.Reason, ActorID: m.ActorID, ActorUsername: m.ActorUsername}
			s.lastOutcome = s.pendingReport
			s.mu.Unlock()
		} else {
			_ = removeMarker(s.opts.DataDirectory)
		}
	default:
		_ = removeMarker(s.opts.DataDirectory)
	}
	return BootResult{}
}

// Confirm must be called once the process is serving. It reports a pending
// rollback immediately, and schedules the health confirmation of a freshly
// installed binary. report is invoked at most once per outcome, from a
// background goroutine, and is where the caller writes the audit event.
func (s *Service) Confirm(report func(Outcome)) {
	s.mu.Lock()
	pending := s.pendingReport
	s.pendingReport = nil
	s.mu.Unlock()
	if pending != nil {
		_ = removeMarker(s.opts.DataDirectory)
		if report != nil {
			report(*pending)
		}
		return
	}
	m, present, err := readMarker(s.opts.DataDirectory)
	if err != nil || !present || m.State != markerApplied || !sameVersion(s.opts.CurrentVersion, m.To) {
		return
	}
	time.AfterFunc(s.opts.ConfirmAfter, func() {
		if err := removeMarker(s.opts.DataDirectory); err != nil {
			s.log.Warn("could not clear update marker", "module", "SelfUpdate.Confirm", "error", err.Error())
			return
		}
		outcome := Outcome{Result: "completed", From: m.From, To: m.To, At: s.now(), ActorID: m.ActorID, ActorUsername: m.ActorUsername}
		s.mu.Lock()
		s.lastOutcome = &outcome
		s.mu.Unlock()
		s.log.Info("update confirmed healthy", "module", "SelfUpdate.Confirm", "from", m.From, "to", m.To)
		if report != nil {
			report(outcome)
		}
	})
}

// RunAutoCheck checks for a new release now-ish and then every interval while
// enabled() is true, until ctx ends. A failed check is remembered and logged
// but never fatal: GameNode has no startup dependency on the release source.
func (s *Service) RunAutoCheck(ctx context.Context, enabled func() bool, initialDelay, interval time.Duration) {
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if enabled == nil || enabled() {
				_, _ = s.Check(ctx)
			}
			timer.Reset(interval)
		}
	}
}

// Bind supplies the callbacks that depend on services constructed after the
// updater itself (the updater must exist early so Boot can run before the
// database is opened). It must be called before the HTTP server starts.
func (s *Service) Bind(activity func(context.Context) (Activity, error), databaseCheck func(context.Context) error, databaseBackup func(ctx context.Context, path string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.Activity, s.opts.DatabaseCheck, s.opts.DatabaseBackup = activity, databaseCheck, databaseBackup
}

func (s *Service) bound() (func(context.Context) (Activity, error), func(context.Context) error, func(context.Context, string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.Activity, s.opts.DatabaseCheck, s.opts.DatabaseBackup
}

// Summary is Status without the safety checks. It is what a periodic poller
// (the dashboard banner) needs: the checks touch the server list and the
// database and only matter to an administrator actually reviewing an update.
func (s *Service) Summary() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snapshotLocked()
	out.CanPrepare = out.Updatable && out.UpdateAvailable && (s.state == StateIdle || s.state == StateFailed || s.state == StateReady)
	return out
}
