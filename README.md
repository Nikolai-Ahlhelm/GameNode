# GameNode

GameNode is a self-contained, single-node game-server management platform for Windows and Linux. It manages existing native applications through a local web interface; it does not require containers, templates, or a central controller.

The current implementation covers the foundation, native runtime, live console, server-root file browser, RBAC, monitoring and health state, auto-restart, port management, audit log, dashboards, typed settings, diagnostics, support bundles, the Official Game Library, safe Egg template import, and native SteamCMD provisioning. Cluster/controller operation, Docker/Podman, a marketplace, automatic server updates, backups, scheduling, and firewall/NAT automation are intentionally out of scope.

## Capabilities

- Register custom native applications or adopt existing installations without moving or modifying them.
- Start, stop, kill, restart, and observe native server processes with PID plus OS start-identity verification.
- View stdout/stderr and send console input over authenticated WebSockets.
- Browse and manage files under each server's configured working directory: list, open, edit, create, upload, download, rename/move, and delete.
- Edit bounded UTF-8 text files in Monaco; `txt`, `json`, `yaml`, `yml`, `xml`, `ini`, `cfg`, and `properties` are supported editor formats.
- Assign allow-only roles to local users and groups at global or server scope.
- Monitor server process health and history, configure bounded auto-restart policies, and register TCP/UDP ports for collision checks before start.
- Inspect append-only audit records, safe diagnostics, typed monitoring settings, and a bounded sanitized support bundle.
- Analyze and persist Pelican/Pterodactyl Eggs as normalized GameNode templates with compatibility reports and native SteamCMD/launch plans.
- Provision supported templates asynchronously through a managed SteamCMD installation, then create an ordinary native GameNode server.
- Adopt an existing Minecraft NeoForge installation through the Official read-only template and a conservative launcher resolver.

## Security model

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

The frontend build is embedded in the executable, so Node.js is not required at runtime. Open `http://127.0.0.1:8443`, or pass `-config path/to/config.yaml` for an explicit configuration file. Configure both `server.tls_cert` and `server.tls_key` in production so session cookies are marked `Secure`.

## Server and file-browser usage

Create a server from **Servers** by supplying an existing working directory, a directly executable native program, an argument array, optional child-process environment values, and a stop timeout. **Adopt Existing** only registers this metadata; it never installs, relocates, or alters server files.

The **Files** tab is scoped to that server's working directory. It supports non-recursive directory listing, bounded text reads and edits, creation, atomic uploads, streaming downloads, rename/move, and explicit recursive deletion. Filesystem authorization and path validation are enforced independently by the backend; frontend path checks are usability affordances only.

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

GameNode is intentionally a local, single-node product. It provides no distributed controller, remote-node protocol, automatic firewall/NAT management, permanent port reservation, container runtime, or generic installer beyond the reviewed native template flows described below. Port availability probes are best effort and retain the normal bind-time TOCTOU window. A process discovered after GameNode restarts can be identity-verified but remains console-detached. Review [docs/runtime.md](docs/runtime.md) and [docs/security.md](docs/security.md) when deploying under a service account.
# Egg template import foundation (v0.2)

GameNode can analyze and import Pelican/Pterodactyl v2 Egg JSON files into a normalized, persisted GameNode template. Eggs are an import format only: the native runtime never reads Egg JSON, executes Egg shell scripts, starts Docker images, or maps container paths onto the host. The Templates UI provides upload, compatibility preview, variable inspection, import, detail, and delete workflows protected by independent global `Templates.View` and `Templates.Manage` permissions.

The importer recognizes a conservative SteamCMD pattern and creates a native installer plan containing the App ID, validation flag, login mode, platform, optional beta-variable references, and the semantic `server_root` target. Startup is imported only when a direct executable and argument array can be extracted safely; shell operators and command substitution are reported and never executed.

# Native SteamCMD provisioning (v0.2)

Templates with a supported anonymous SteamCMD plan and a safe launch definition can be provisioned from the Templates UI. GameNode bootstraps SteamCMD from a fixed official Valve HTTPS source into `<data>/tools/steamcmd`, installs game files into `<data>/servers/<directory>`, expands only declared template variables, and transactionally creates a normal GameNode server after installation succeeds. Jobs expose bounded phase/status information, support cancellation, prevent concurrent use of the same target, and retain a clear `files_may_remain` signal after failure.

Egg scripts, arbitrary URLs, free-form SteamCMD flags, credentialed login, Docker images, and update-on-start hooks are not executed. Sensitive values are masked by the server API and excluded from audit/support output; this version stores environment values in the existing SQLite server record without application-level at-rest encryption. Provisioning jobs interrupted by a GameNode restart are marked failed rather than resumed. See [architecture](docs/architecture.md), [security](docs/security.md), and [API](docs/api.md) for the precise boundary.

# Official Game Library and Minecraft NeoForge (v0.2)

The Game Library loads a schema-versioned manifest from the repository's fixed [GitHub Raw `main` path](https://raw.githubusercontent.com/Nikolai-Ahlhelm/GameNode/main/templates/catalog.json). Official JSON lives under [`templates/`](templates/README.md), remains Git-reviewed in this repository, and is not duplicated into user-editable SQLite template rows. GameNode shows a validated last-good cache immediately from `<data>/templates/cache`, refreshes when the library opens or when requested, and keeps imported Eggs usable during a GitHub outage. There are no community sources, user-configurable catalog URLs, GitHub tokens, hashes, or signatures in this milestone.

The Official catalog includes **Minecraft NeoForge**, **7 Days to Die**, and **Project Zomboid**. NeoForge's Adopt Existing flow derives a direct Java process without invoking its generated scripts. The Steam games use fixed catalog-owned App IDs, typed launch values, and declared ports. Project Zomboid App ID `380870` is currently declared Windows-only: GameNode invokes its installed `jre64/bin/java.exe` with the vendor JSON's fixed JVM/classpath definition instead of executing `StartServer64.bat`, confines generated data below the server root, and stops with the documented stdin `quit` command. App IDs, login commands, URLs, and SteamCMD flags are never user input. A normal server is committed only after SteamCMD succeeds and the platform-specific executable is verified inside the managed root.

Official games may also ship versioned declarative configuration adapters in their own game directory. 7 Days to Die maps selected settings to `serverconfig.xml`; Project Zomboid template `1.1.0` maps a reviewed subset of its generated `Server/gamenode.ini`. GameNode persists the exact adapter with each server and exposes typed Game Settings on the Configuration tab. A post-start adapter remains clearly pending until the game creates its file. Remote JSON may select only the compiled `xml-properties` or strict flat `ini-key-values` implementation; it cannot provide XPath, parser code, scripts, hooks, or escaping paths. Writes are bounded, format-aware, backed up, atomic, audit-recorded, and never return secret values. Project Zomboid's executable `SandboxVars.lua` remains unmanaged.

The template does not download Minecraft or NeoForge, write `eula.txt`, overwrite the server directory, or interpret arbitrary launcher syntax. It verifies but does not execute free-form `user_jvm_args.txt`; typed minimum/maximum memory replace the reference file's empty defaults. It also supports `nogui` and graceful `stop` over the attached console with a timeout/kill fallback. The local `server-test` reference resolves as NeoForge `26.2.0.59` for Minecraft `26.2`.
