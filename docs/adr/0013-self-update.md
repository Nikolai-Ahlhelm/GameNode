# ADR 0013: GameNode Self-Update (local and remote)

## Status

Accepted (backend, machine-authenticated Node API, controller API, and UI
implemented and tested; see "Verification" and "Known limitations").

## Context

GameNode is a single-process, single-binary product distributed as GitHub
release assets (`gamenode-windows-amd64.exe`, `gamenode-linux-amd64`,
`SHA256SUMS.txt`; `.github/workflows/release.yml`). Until now, updating a
node meant an operator noticing a release, downloading the right file by hand,
stopping the process, replacing the executable, and starting it again - on
every node of a cluster.

The requirement is an administrator-driven update that (a) notices new
releases and prompts the administrator, (b) can be performed from the
GameNode UI, (c) can be triggered on an enrolled remote node from its
controller, and (d) is guarded by safety checks that run before anything is
replaced.

This feature replaces the running executable of a process that is allowed to
launch arbitrary configured executables, so its trust model matters more than
its convenience. It must not become a remote-code-execution primitive, a
downgrade or supply-chain vector beyond what installing a release by hand
already is, or a way to damage running game servers.

## Decision

### One narrow domain: `internal/selfupdate`

A transport-free package with no dependency on HTTP, RBAC, servers,
provisioning, or the database driver. Everything it needs from the rest of the
process (workload facts, a database health check and backup) reaches it as
callbacks composed in `cmd/gamenode`. It is deliberately not a generic update
engine: it updates exactly one thing (this binary), from exactly one place,
only when an administrator says so.

### Fixed source; URLs are never data

The source is `github.com/Nikolai-Ahlhelm/GameNode`, fixed in code like the
Official Game Library and SteamCMD sources. There is no GameNode setting for
a repository, mirror, URL, or token. (Like any Go HTTP client, requests honor
the host's standard `HTTPS_PROXY`/`NO_PROXY` environment, which is how an
administrator routes GameNode through a corporate proxy.)

- Only `GET /repos/{owner}/{repo}/releases/latest` is used, so drafts and
  prereleases are never offered.
- The API response contributes only the tag, display metadata, and asset
  names/sizes. **Download URLs are constructed** from the validated tag and the
  fixed asset name for this platform - the `browser_download_url` in the
  response is never used. The tag must match a strict `vMAJOR.MINOR.PATCH[-pre]`
  pattern before it can reach a URL.
- Redirects (GitHub serves assets from `*.githubusercontent.com`) are followed
  only over HTTPS to GitHub-owned hosts, at most five.
- Every response and download is size-bounded; an oversized stream fails
  instead of being truncated.

### Verification before anything is installed

Downloading and installing are separate steps (`prepare`, then `apply`).
`prepare` stages the binary beside the executable (never in a shared temp
directory) and requires all of:

1. the checksum manifest (`SHA256SUMS.txt`) is fetched **first**, and must
   contain exactly one consistent entry for this platform's asset;
2. the downloaded byte count equals the size the release declares;
3. the SHA-256 of the download equals the manifest entry;
4. the file is a plausible executable for this OS and CPU (ELF x86-64 or PE
   AMD64, above a minimum size);
5. running the staged binary with the fixed argument `--version` (structured
   exec, no shell) prints `gamenode <version>` equal to the release version.
   This proves it loads and runs on this host and is the release it claims to
   be.

Any failure removes the staged file. Nothing is installed by `prepare`.

`apply` re-hashes the staged file immediately before use, so a file replaced
between the two steps is discarded, never installed.

### Safety checks (preflight)

Each check has a stable id and one of three results: `pass`, `warn` (may
proceed only if the administrator explicitly acknowledges), or `block` (can
never be overridden). They run when an update is proposed, again before the
download, and **again immediately before the swap**:

| Check | Result when unmet |
| --- | --- |
| `platform` - an official binary exists for this OS/arch | block |
| `release_build` - running an official, versioned build (not `dev`) | block |
| `newer_version` - target is strictly newer (downgrades and reinstalls are never offered) | block |
| `release_assets` - the release publishes the binary and the manifest | block |
| `executable_writable` - the installation directory is writable by this process | block |
| `disk_space` - room for the download and a database backup (per volume) | block (warn if unmeasurable) |
| `active_jobs` - no provisioning job and no manual server-update job running | block |
| `server_transitions` - no server starting or stopping | block |
| `database` - `PRAGMA quick_check` passes | block |
| `artifact` - a verified, untampered staged binary for this exact release | block |
| `running_servers` - servers are running | warn |
| `restart_mode` - configured to exit and rely on a supervisor | warn |

A blocked apply leaves the verified download staged so the administrator can
retry once the condition clears. The API refuses an apply with warnings unless
`acknowledge_warnings` is true, and refuses regardless when any check blocks.

### Installing, restarting, and running servers

`apply` (1) takes a consistent database copy (`VACUUM INTO`) under
`<data>/updates/backups/` (newest three kept), (2) renames the running
executable to `<name>.previous`, (3) renames the staged binary into place, and
(4) writes the rollback marker. Renaming a running executable is permitted on
both Windows and Linux; the running process is undisturbed. A failure at any
step restores the previous state, and a failed marker write undoes the swap -
an install that cannot be rolled back is not performed.

The process then shuts down gracefully (`http.Server.Shutdown`), runs every
deferred cleanup (database, FTP, scheduler, jobs), and only **then** hands
over: on Linux it replaces itself in place with `syscall.Exec` (same PID, so a
supervisor sees no restart); on Windows it starts a detached successor. The
hand-over is performed by `main` after `run` returns, so the successor never
competes with a half-closed predecessor for the database or listeners.

This is an ordinary GameNode restart from the game servers' point of view
(docs/runtime.md): managed processes are not tied to GameNode's lifetime, are
rediscovered by verified identity, and come back with their console detached.
That detachment is why running servers produce an acknowledgeable **warning**
rather than a silent success. An `update.restart_mode: exit` option in the
operator's config file (never settable through the API) makes GameNode exit
cleanly instead, for installations run under a supervisor.

The executable path is captured at startup, before any swap: on Linux,
`os.Executable` afterwards reports the renamed backup.

### Automatic rollback of an unhealthy update

`<data>/updates/pending.json` (a file, not SQLite - the database schema is one
of the things an update may break) records `from`, `to`, and a count of starts.
A new binary that never reaches its health confirmation (60 seconds of
continuous serving after the listener is up) is counted on each start; the
third unconfirmed start restores `<name>.previous` and relaunches it instead
of running the new binary. The restored binary reports the rollback exactly
once. The failed binary is kept as `<name>.failed`. A corrupt or stale marker
is discarded so it can never trap startup in a loop, and a rollback with no
usable backup is dropped rather than retried.

### Audit

- `system.update_apply` - once per install request, with its synchronous
  outcome (success, or a controlled failure code such as `preflight_blocked`),
  actor, from/to versions, and whether warnings were acknowledged.
- `system.update_complete` / `system.update_rollback` - exactly once, by the
  process that observes the outcome after the restart, attributed to the
  administrator who requested the install.
- `node.software_update` - the controller-side record of an install requested
  on an enrolled node (the node records its own `system.update_apply`,
  attributed to `controller`).

Checking for updates and downloading are routine and not audited. Metadata is
version strings and a boolean; failure summaries are controlled text, never
raw external output.

### RBAC

Two new, **global-only** permissions, independent of `Settings.*`:
`Update.View` (see version/status) and `Update.Manage` (check, download,
install). Installing replaces the running binary, which is a stronger power
than editing settings, so it is not folded into `Settings.Manage`. Neither
implies the other.

Remote updates require **both** halves: `Node.View` + `Update.View` to read a
node's update status, `Node.Manage` + `Update.Manage` to download or install on
it. Registry administration does not authorize replacing a node's software,
and vice versa. The `self_update` capability is advertised only by builds that
implement the endpoints; a controller treats its absence as "unsupported" and
never contacts the node.

### Remote update

`/api/v1/node/update[/check|prepare|apply|cancel]` sits in the existing
machine-authenticated Node trust domain (bearer credential, no cookie, no
CSRF, no RBAC - like every `/api/v1/node/*` endpoint). It forwards straight
into that node's own `selfupdate.Service`.

**The controller never supplies a binary, URL, checksum, or path.** Its request
carries a version string (and the warning acknowledgement); unknown request
fields are rejected. The node fetches the release itself from the fixed source
and runs its own safety checks. A compromised or malicious controller
credential can therefore at most ask a node to install the newest official
release - which the controller could already achieve, and far less than it can
already do by creating a server with an arbitrary executable.

The controller relays only whitelisted error codes with its own message text
(a remote node's text is never echoed), bounds and normalizes the checks it
relays (at most 32, bounded text, unknown status treated as `block`), and uses
a longer-timeout client for these calls only (`remote.SlowTimeout`), with the
same TLS verification and redirect refusal as every other remote call.

### Discovery and prompting

A background loop checks about every 12 hours (first check two minutes after
start) while `updates.auto_check` is enabled (default on; `Settings.Manage` can
turn it off). A failed check is recorded and logged; GameNode has no startup
dependency on the release source. Users with `Update.View` see a dashboard
banner for a newer release, dismissible per release (a per-browser
convenience stored only in `localStorage`); the full workflow is under
Settings > Updates and, per node, on the node's detail page.

### `--version`

`gamenode --version` prints `gamenode <version>` and exits before loading
configuration or touching any data. It is the contract the self-test relies on.

## Consequences

- **Trust anchor.** Integrity rests on TLS to GitHub plus GitHub-hosted release
  assets and a checksum manifest hosted next to them. The checksum defends
  against corruption, truncation, a wrong-platform asset, and a tampered
  mirror or proxy path - it does **not** defend against a compromised GitHub
  account or release, because an attacker who can replace the binary can
  replace the manifest. Detached signatures (for example an Ed25519 key pinned
  in the binary and a release-pipeline signing step) would close that gap and
  are the recommended follow-up; they were not added here because they require
  release-infrastructure and key-management decisions that are not this
  change's to make.
- **Bootstrap.** Only a release that contains this feature supports
  `--version`, so an installation must be updated *to* the first such release
  by hand once. Verified against the real 1.1.0 release: it downloads, matches
  its checksum, and is then correctly refused by the self-test.
- **Database rollback is not automatic.** The binary is restored; the database
  is not (that would silently discard data written since the update). The
  pre-update backups are kept for a manual restore, and a restored older binary
  runs against a database a newer version already migrated.
- **A health confirmation is a liveness signal, not a functional test.** A
  binary that serves for 60 seconds but misbehaves is confirmed.
- **Windows service wrappers** that treat any exit of the launched process as
  "stopped" will see the original process exit as the successor starts; use
  `update.restart_mode: exit` with a wrapper that restarts on exit. The
  successor is detached from the console, so console output of an interactively
  started instance goes to the log files after an update.
- **A small race remains** between the final safety check and the restart:
  a provisioning job started in that window is interrupted by the restart and
  becomes `failed/interrupted` under the existing recovery rules (jobs are
  never resumed, and nothing is registered without validation).
- Installations where the executable directory is read-only (packaged,
  containerized) fail the `executable_writable` check by design; they are
  updated by replacing the package or image.

## Verification

- `internal/selfupdate`: unit tests for version precedence, checksum parsing,
  the GitHub source (projection, error classification, fixed URLs, size and
  redirect bounds), every preflight check, tamper detection, cancellation, and
  boot/rollback/confirmation logic; plus **end-to-end tests that build real
  binaries and let them update, roll back, and reject a corrupted release as
  real processes** (real executable swap of a running binary, real
  `--version` self-test, real relaunch).
- `internal/api`: RBAC/CSRF matrix for the local, node, and controller
  endpoints, audit attribution, request-field rejection, error-text isolation.
- `internal/remote`: typed errors, fixed paths, redirect refusal.
- A workflow-contract test fails if `release.yml` stops publishing the assets
  the updater downloads.
- Manually, against the real `cmd/gamenode` binary and the real GitHub release
  API: version check, download and self-test refusal of a pre-`--version`
  release, and a full install-and-restart between two real builds (process
  replaced, port and SQLite database handed over, audit events recorded once).
