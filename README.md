# GameNode

GameNode is a self-contained, single-node game-server management platform for Windows and Linux. It manages native applications and explicit Linux-first Docker Container servers through a local web interface; it does not require a central controller.

The current implementation covers the foundation, Native and Linux-first Docker Container runtimes, live console, server-root file browser, RBAC, tenant-scoped multi-tenancy, monitoring and health state, auto-restart, port management, audit log, dashboards, typed settings, diagnostics, support bundles, the Official Game Library, safe Egg template import, native SteamCMD provisioning, container-backed Egg provisioning, and manual SteamCMD server updates. Container servers use explicit image pull, typed CPU/RAM limits, host-to-container port mappings, and ownership-safe rediscovery. Container Egg installation/startup scripts run only inside a short-lived, unprivileged, resource- and time-bounded container with the managed server root mounted; they never run on the host. SteamCMD supports initial provisioning and explicit manual updates of eligible existing SteamCMD-managed servers; automatic, scheduled, and update-on-start behavior remains unsupported. When an existing database has pending schema migrations, startup creates a consistent `*.pre-migration-*.db` SQLite copy beside it before changing the schema; this is an upgrade safeguard, not a general backup system. Docker CLI execution, privileged mode, arbitrary mounts, socket mounts, registry credentials, cluster/controller operation, marketplace functionality, automatic updates, backups, scheduling, and firewall/NAT automation remain out of scope.

## Capabilities

- Register custom native applications or adopt existing installations without moving or modifying them.
- Start, stop, kill, restart, and observe native server processes with PID plus OS start-identity verification.
- View stdout/stderr and send console input over authenticated WebSockets.
- Browse and manage files under each server's configured working directory: list, open, edit, create, upload, download, rename/move, and delete.
- Edit bounded UTF-8 text files in Monaco; `txt`, `json`, `yaml`, `yml`, `xml`, `ini`, `cfg`, and `properties` are supported editor formats.
- Organize servers into tenants - logically separate customer or organization boundaries with their own managed storage location, membership roster, and administration UI. Every server belongs to exactly one tenant, set at creation and immutable afterward.
- Assign allow-only roles to local users and groups at global, tenant, or server scope.
- Build scope-neutral roles from the authoritative permission catalog; the UI identifies tenant- and server-assignable roles and explains global-only or mixed-role incompatibilities.
- Monitor server process health and history, configure bounded auto-restart policies, and register TCP/UDP ports for collision checks before start.
- Configure the instance name, subtitle, local PNG/ICO favicon, monitoring, logging, and password policy from typed settings. New passwords default to 8–256 characters, with administrator-configurable bounds.
- Switch between dark, light, and system themes, collapse the sidebar, and optionally set a local background wallpaper (PNG/JPEG/WebP, processed and validated entirely in the browser) from Settings → Appearance. These are personal, browser-local preferences, not shared instance settings.
- Analyze and persist Pelican/Pterodactyl Eggs as normalized GameNode templates with compatibility reports and native SteamCMD/launch plans.
- Provision supported templates asynchronously through a managed SteamCMD installation, then create an ordinary native GameNode server.
- Manually update an eligible, already-provisioned SteamCMD-managed server's installed game files in place, without migrating its pinned template, ports, or configuration.
- Adopt an existing Minecraft NeoForge installation through the Official read-only template and a conservative launcher resolver.
- Enroll another GameNode installation as a Remote Node: durable node identity, secure pairing-token enrollment, an authenticated machine-to-machine Node API, health/capability status, remote server create/edit/delete/lifecycle control, live console relay with polling fallback, sandboxed binary/text files, and remote monitoring. See [Remote Nodes](#remote-nodes-v05a) below.

## Security model

The dashboard can administer the existing RBAC model: reusable roles draw from the backend permission catalog, then administrators assign roles to users or groups globally, per tenant, or per server. Global-only permissions cannot be saved as tenant or server assignments. Grants are explicit: `Manage` never implies `View`.

Tenant boundaries are an API/application access-control boundary, not OS-level process isolation: every server, regardless of tenant, runs as a plain native process under the same OS account as the GameNode service. See the [security guide](docs/security.md) before treating tenants as isolation for mutually distrusting operators.

The backend is the authorization and filesystem security boundary.

- Authentication uses opaque, HttpOnly, SameSite=Strict session cookies; passwords use Argon2id.
- RBAC is allow-only. Server permissions are independent: for example, `Files.Edit` does not grant `Files.View`, `Files.Download`, `Files.Rename`, or `Files.Delete`.
- All authenticated mutations require same-origin validation and `X-CSRF-Token`.
- The file browser accepts only server-root-relative paths. It rejects traversal, absolute paths, drive/UNC paths, separator tricks, and URL-decoded traversal. Linux symlink targets must resolve within the canonical root; Windows reparse points and junctions are conservatively denied.
- Server processes are launched from structured executable and argument values, never a user-provided shell command string.
- Audit metadata is controlled and bounded; passwords, session values, CSRF tokens, environment values, file contents, upload bytes, and console input are not recorded.

Read the detailed [security guide](docs/security.md), [filesystem API contract](docs/api.md), and [native runtime limitations](docs/runtime.md) before production use.

## Requirements

- Go 1.23 or newer
- Node.js 22 or newer and npm (development and frontend builds only)
- Windows amd64 or Linux amd64 for the supported release artifacts

## Quick start

Copy the example configuration and start the API:

```powershell
Copy-Item config.example.yaml config.yaml
go run ./cmd/gamenode -config config.yaml
```

For frontend development, start Vite in another terminal:

```powershell
Set-Location web
npm ci
npm run dev
```

Open the Vite URL, complete the one-time administrator setup, then use the local interface. Vite proxies `/api` to `http://127.0.0.1:8443`.

Without a configuration file, GameNode listens on `127.0.0.1:8443` and stores SQLite data under `./data`. The example configuration contains the listener, data/database locations, logging level, maximum multipart upload size, and monitoring defaults. Server definitions belong in SQLite, not in YAML.

## Production-style local build

Build the embedded frontend before compiling the Go binary:

```powershell
Push-Location web
npm ci
npm run build
Pop-Location
go build -o gamenode.exe ./cmd/gamenode
```

The frontend build is embedded in the executable, so Node.js is not required at runtime. On its first start, the executable creates `config.yaml` beside itself when no `-config` path is supplied. Before the first administrator is created, the guided setup offers the default data and database paths or lets an operator choose absolute alternatives; a restart applies changed paths. It can then optionally prepare SteamCMD from Valve's fixed source. Open `http://127.0.0.1:8443`, or pass `-config path/to/config.yaml` for an explicit configuration file, which is also created with defaults when absent. Configure both `server.tls_cert` and `server.tls_key` in production so session cookies are marked `Secure`.

For TLS terminated by a reverse proxy on the same machine, keep GameNode bound to loopback and set `server.trust_local_proxy: true`. GameNode then accepts `X-Forwarded-Proto` and `X-Forwarded-Host` only from `127.0.0.1` or `::1`, and marks session cookies `Secure`. Do not enable this setting for a proxy reached over the network.

## Server and file-browser usage

Create a server from **Servers** by supplying an existing working directory, a directly executable native program, an argument array, optional child-process environment values, and a stop timeout. **Adopt Existing** only registers this metadata; it never installs, relocates, or alters server files.

The **Files** tab is scoped to that server's working directory. It supports non-recursive directory listing, bounded text reads and edits, creation, atomic uploads, streaming downloads, rename/move, and explicit recursive deletion. Filesystem authorization and path validation are enforced independently by the backend; frontend path checks are usability affordances only.

### Scheduled restarts

Server Detail includes **Scheduled Restarts** for operators with `Server.Edit`.
Schedules are typed daily or weekly recurrences with an explicit IANA timezone,
for example `Every Sunday · 04:00 · Europe/Berlin`. They are local to this
GameNode, persist in SQLite, and invoke the same `servers.Service.Restart`
lifecycle used by a manual restart. A stopped or otherwise ineligible server is
not started implicitly; the attempt is logged and audited as a scheduled
restart. Missed occurrences are skipped after a GameNode restart, and a
nonexistent DST wall-clock time is skipped rather than retried.

Scheduled restarts are not a generic job system: they do not run commands,
pull images, update SteamCMD, provision Eggs, or schedule work on Remote Nodes.

## Verification

### Latest Windows acceptance result (2026-08-11)

On a native Windows amd64 machine, `npm ci`, `npm run check`, `npm run test:helpers`, `npm run build`, `go vet ./...`, `go build ./...`, and `go test ./...` passed. Fresh embedded-release-binary smoke testing covered first-run setup, login/logout, server CRUD, lifecycle, console output/input, RBAC and filesystem operations. The current Windows harnesses produced `E2E_WEBSOCKET_OK`, `E2E_MILESTONE3_OK`, `E2E_RBAC_MILESTONE5B_OK`, and `E2E_FILESYSTEM_MILESTONE4_OK`; Windows junction escape rejection passed. Creating a symbolic link was skipped because the test account lacks the Windows symlink privilege. The Linux amd64 artifact cross-build passed; no Linux runtime was available for a native smoke. Native race testing was unavailable because this Windows installation has neither CGO nor `gcc`.

Run Go formatting, static checks, tests, frontend checks, and release builds:

```powershell
gofmt -w (Get-ChildItem -Recurse -File -Filter *.go | ForEach-Object FullName)
go vet ./...
go test ./...
go test -race ./...

Push-Location web
npm ci
npm run check
npm run test:helpers
npm run build
Pop-Location

New-Item -ItemType Directory -Force dist | Out-Null
$env:GOOS='windows'; $env:GOARCH='amd64'; go build -o dist/gamenode-windows-amd64.exe ./cmd/gamenode
$env:GOOS='linux'; $env:GOARCH='amd64'; go build -o dist/gamenode-linux-amd64 ./cmd/gamenode
Remove-Item Env:GOOS, Env:GOARCH
```

The race detector must run natively on the target OS/architecture; CI runs it on Linux amd64. See [development](docs/development.md) for local verification and support-bundle smoke testing, and [CI](docs/ci.md) for the workflow and artifacts.

## CI and releases

Pull requests targeting `main` and pushes to `main` run the GitHub Actions CI workflow. It verifies Go formatting, vet, tests, a Linux race-detector pass, native Windows tests, frontend type/helper tests, and production frontend builds. Successful `main` runs expose unsigned Windows amd64 and Linux amd64 development binaries as workflow artifacts for 14 days.

To publish an official release, push a semantic version tag such as `v0.1.0`. The release workflow repeats the required verification, builds the frontend before each final Go binary so its assets are embedded, and publishes these assets:

- `gamenode-windows-amd64.exe`
- `gamenode-linux-amd64`
- `SHA256SUMS.txt`

Release binaries expose the tag in Diagnostics and also include the build commit and UTC build time. Verify the SHA-256 checksums after downloading an asset. See [CI](docs/ci.md) for the exact job structure and release semantics.

## Documentation

- [Architecture](docs/architecture.md)
- [REST and WebSocket API](docs/api.md)
- [Security model](docs/security.md)
- [Native runtime behavior and limitations](docs/runtime.md)
- [Development and verification](docs/development.md)
- [Architecture decisions](docs/adr/)
- [Project plan and non-goals](PROJECT_PLAN.md)

## Operational limitations

GameNode is intentionally a local-first, single-node-autonomous product. It now has a Remote Node foundation plus remote server/console/files/monitoring API and Nodes UI surfaces (see below); remote binary transfers are bounded and sandboxed, console relay has a polling fallback, and there is no automatic firewall/NAT management or permanent port reservation. Port availability probes are best effort and retain the normal bind-time TOCTOU window. A process discovered after GameNode restarts can be identity-verified but remains console-detached. Review [docs/runtime.md](docs/runtime.md) and [docs/security.md](docs/security.md) when deploying under a service account.

# Remote Nodes (v0.5A)

Every GameNode installation remains fully autonomous - its own SQLite database, API, UI, and lifecycle authority - whether or not it is ever enrolled with a controller. A controller talks to a Remote Node only through an authenticated Node API; it never opens the remote node's database or controls its Docker/process runtime directly.

Enrollment is deliberate and pairing-based: an operator generates a single-use, 15-minute pairing token on the node being enrolled (`Node.Manage`), and an operator on the controller side supplies that token plus the node's endpoint to enroll it. A successful enrollment issues a durable machine credential (never a browser session/CSRF token) that authenticates future `GET /api/v1/node/info|health|capabilities` calls. The controller's Nodes UI shows identity, protocol/version compatibility, capabilities, health, and last contact for each enrolled node, refreshed on a bounded periodic schedule.

v0.5A is the read-only foundation: node identity, pairing/enrollment, the Node API, the remote-node registry, health/capability status, and the Nodes UI. See [`docs/adr/0007-remote-node-foundation.md`](docs/adr/0007-remote-node-foundation.md) for the full trust model.

v0.5B (Remote Server Management) and v0.5C (Remote Operational Hardening) add remote server create/edit/delete/start/stop/restart/kill, live/fallback remote console, sandboxed text/binary files (`RemoteFiles.View`/`.Edit`/`.Upload`/`.Download`/`.Delete`/`.Rename`), and remote monitoring - all through the same machine-authenticated Node API pattern, forwarded to the target node's own `servers.Service`/filesystem sandbox, never a second lifecycle implementation. The backend, tests, and controller-facing Nodes UI are complete. See [`docs/adr/0010-remote-server-lifecycle-forwarding.md`](docs/adr/0010-remote-server-lifecycle-forwarding.md) and [`docs/adr/0011-remote-operational-hardening.md`](docs/adr/0011-remote-operational-hardening.md).

# Egg template import foundation (v0.2)

GameNode can analyze and import Pelican/Pterodactyl v2 Egg JSON files into a normalized, persisted GameNode template. Native and Container compatibility are reported separately. Native provisioning never reads Egg scripts; an explicitly selected Container path may run a bounded, reviewed Egg installation/startup plan only inside the controlled unprivileged installer/server container boundary. The Templates UI provides upload, compatibility preview, variable/image inspection, import, detail, and delete workflows protected by independent global `Templates.View` and `Templates.Manage` permissions.

The importer recognizes a conservative SteamCMD pattern and creates a native installer plan containing the App ID, validation flag, login mode, platform, optional beta-variable references, and the semantic `server_root` target. Startup is imported only when a direct executable and argument array can be extracted safely; shell operators and command substitution are reported and never executed.

# Native SteamCMD provisioning (v0.2)

Templates with a supported anonymous SteamCMD plan and a safe launch definition can be provisioned from the Templates UI. GameNode bootstraps SteamCMD from a fixed official Valve HTTPS source into `<data>/tools/steamcmd`, installs game files into `<data>/servers/<directory>`, expands only declared template variables, and transactionally creates a normal GameNode server after installation succeeds. Jobs expose bounded phase/status information, support cancellation, prevent concurrent use of the same target, and retain a clear `files_may_remain` signal after failure.

Native Egg scripts, arbitrary URLs, free-form SteamCMD flags, credentialed login, and update-on-start hooks are not executed. Container Egg installation uses only declared allowlisted images, explicit pulls, fixed resource/mount policy, bounded output/timeouts, and no registry credentials. Sensitive values are masked by the server API and excluded from audit/support output; this version stores environment values in the existing SQLite server record without application-level at-rest encryption. Provisioning jobs interrupted by a GameNode restart are marked failed rather than resumed; installed container targets can use the existing owner-only registration-recovery flow. See [architecture](docs/architecture.md), [security](docs/security.md), and [API](docs/api.md) for the precise boundary.

# Manual SteamCMD server updates (v0.2.1)

SteamCMD supports initial provisioning and explicit manual updates of eligible existing SteamCMD-managed servers. Automatic, scheduled, and update-on-start behavior remains unsupported. An operator can trigger "Update Server" from Server Detail on an already-provisioned, stopped SteamCMD server; GameNode re-runs the same structured `+force_install_dir`/`+login anonymous`/`+app_update`/`+quit` invocation against the server's existing managed root, using only the App ID, validate flag, and template provenance captured once at provisioning time. Updating the Steam depot never migrates the server's pinned template version, executable, arguments, environment, ports, stop behavior, or configuration adapter snapshots - those stay exactly as provisioned even if the Official catalog has since published a newer template version. The server must already be stopped; GameNode never stops or restarts it automatically. The update runs as a persisted, cancellable job and validates the launch executable still exists safely afterward. Servers provisioned before this metadata existed, or provisioned outside the SteamCMD path, are reported ineligible rather than guessed from directory contents.

# Official Game Library and Minecraft NeoForge (v0.2)

The Game Library loads a schema-versioned manifest from the repository's fixed [GitHub Raw `main` path](https://raw.githubusercontent.com/Nikolai-Ahlhelm/GameNode/main/templates/catalog.json). Official JSON lives under [`templates/`](templates/README.md), remains Git-reviewed in this repository, and is not duplicated into user-editable SQLite template rows. GameNode shows a validated last-good cache immediately from `<data>/templates/cache`, refreshes when the library opens or when requested, and keeps imported Eggs usable during a GitHub outage. There are no community sources, user-configurable catalog URLs, GitHub tokens, hashes, or signatures in this milestone.

The Official catalog includes **Minecraft NeoForge**, **7 Days to Die**, **Project Zomboid**, **Palworld**, **Satisfactory**, and **Eco**. NeoForge's Adopt Existing flow derives a direct Java process without invoking its generated scripts. The Steam games use fixed catalog-owned App IDs, typed launch values, and declared ports. Project Zomboid, Palworld, and Satisfactory are currently declared Windows-only. Satisfactory launches `FactoryServer.exe` directly with structured primary-port, reliable-port, and player-limit arguments; server claim and passwords remain in the game's own Server Manager rather than becoming ineffective template variables. App IDs, login commands, URLs, and SteamCMD flags are never user input. A normal server is committed only after SteamCMD succeeds and the platform-specific executable is verified inside the managed root.

Official games may also ship versioned declarative configuration adapters in their own game directory. 7 Days to Die maps selected settings to `serverconfig.xml`; Project Zomboid template `2.0.0` maps a reviewed subset of its generated `Server/gamenode.ini`; Palworld uses the compiled `section-tuple-key-values` format and safely seeds its missing target from the installed default. GameNode persists the exact adapter with each server and exposes typed Game Settings on the Configuration tab. A post-start adapter remains clearly pending until the game creates its file. Remote JSON may select only the compiled `xml-properties`, strict flat `ini-key-values`, or bounded `section-tuple-key-values` implementation; it cannot provide XPath, parser code, scripts, hooks, or escaping paths. Writes are bounded, format-aware, backed up, atomic, audit-recorded, and never return secret values. Project Zomboid's executable `SandboxVars.lua` remains unmanaged.

The template does not download Minecraft or NeoForge, write `eula.txt`, overwrite the server directory, or interpret arbitrary launcher syntax. It verifies but does not execute free-form `user_jvm_args.txt`; typed minimum/maximum memory replace the reference file's empty defaults. It also supports `nogui` and graceful `stop` over the attached console with a timeout/kill fallback. The local `server-test` reference resolves as NeoForge `26.2.0.59` for Minecraft `26.2`.
