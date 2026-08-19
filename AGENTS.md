# GameNode Agent Guide

This file is the implementation guide for coding agents working in this repository. Read it before changing code. It records constraints that are easy to lose when reading a single package; deeper behavior belongs in the linked documents and tests.

## 1. Product Summary

GameNode is a self-contained, single-node game-server manager for Windows and Linux. One Go process serves the API and the embedded React/TypeScript/Vite UI, persists to SQLite, and manages Native plus Linux-first Docker Container applications without requiring a controller. Users can still register existing native applications without using templates.

The current v0.4 direction adds separate Native/Container compatibility for conservative Pelican/Pterodactyl Egg import and controlled Container provisioning. Templates still feed ordinary `servers.Server` records; Container is a runtime selection, not an Egg-specific lifecycle.

## 2. Non-Goals and Hard Boundaries

- Keep the deployable architecture a single Go process with SQLite. Do not introduce microservices, Redis, RabbitMQ, Kafka, Kubernetes, or a cluster/controller protocol.
- v0.3 permits the Linux-first Docker Container Runtime beside Native. v0.4 permits controlled Egg installation only inside a short-lived unprivileged Container. `servers.Service` remains lifecycle authority; Docker Engine API is the only control boundary (never Docker CLI/shell). Container identity is managed/server-ID/generation/durable-token labels persisted in StartKey and must be verified before lifecycle, metrics, rediscovery, or ConsoleManager attach. Host and container ports are distinct; image Pull is explicit and Start never pulls/updates. No user-controlled raw engine flags, privileged mode, socket mounts, arbitrary mounts, devices, capabilities, host namespaces, registry credentials, Remote Nodes, or cluster scheduling.
- Do not introduce implicit shell execution, generic script hooks, arbitrary download URLs, arbitrary remote-code-execution endpoints, or user-controlled command strings. GameNode intentionally runs configured native executables, but launch remains structured and permission-gated.
- Do not build speculative provider layers, plugin systems, repositories, generic schedulers, update engines, or other future abstractions without a current requirement. The typed local restart scheduler is the deliberately narrow exception implemented for the current milestone.
- Do not silently implement the next milestone. Automatic game updates, backups, scheduling, firewall/NAT automation, marketplace/community catalogs, credentialed Steam login, and multi-node management remain out of scope.

## 3. Repository Map

- `cmd/gamenode`: composition root, configuration/startup, embedded production UI, HTTP server.
- `internal/api`: HTTP/WebSocket transport, request validation, authorization, CSRF, response mapping, and API-layer audit attribution. Business rules should not accumulate here.
- `internal/auth`: local authentication, setup, opaque cookie sessions, CSRF tokens.
- `internal/identity`: local users, groups, and memberships.
- `internal/rbac`: allow-only permission catalog, roles, assignments, evaluator.
- `internal/servers`: server records and normal lifecycle orchestration; coordinates runtime, console, monitoring, ports, and auto-restart. Every server carries an immutable `TenantID` owned by `internal/tenants`.
- `internal/tenants`: the Tenant Foundation domain. Persistent, transport-independent tenants and tenant memberships; tenant CRUD and membership add/list/remove. Membership alone grants no RBAC permission. `TenantServerRoot` is the single resolver for managed/provisioned storage (`<data>/tenants/<tenant-id>/servers/<directory>`); it independently revalidates both the tenant ID and directory name and never trusts an earlier caller's checks.
- `internal/runtime`: transport-free native process start/status/metrics/stop/kill; OS implementations live in platform files.
- `internal/console`: bounded in-memory console sessions, history, subscribers, and stdin.
- `internal/filesystem`: authoritative server-root sandbox and all file-browser operations.
- `internal/monitoring`: bounded process sampling and derived health.
- `internal/ports`: port registry, collision checks, and best-effort OS availability probes.
- `internal/audit`: append-only audit model, catalog, persistence, filters, and limits.
- `internal/settings`, `internal/diagnostics`, `internal/support`: typed persisted settings, safe diagnostics, and whitelisted support bundles.
- `internal/templates`: normalized template domain, Egg analyzer/import, SQLite store, built-ins, official catalog/cache, and NeoForge resolver.
- `internal/steamcmd`: fixed-source managed SteamCMD bootstrap, safe extraction, and structured invocation.
- `internal/provisioning`: persisted asynchronous provisioning jobs and normal-server creation.
- `internal/serverupdates`: manual SteamCMD update jobs for already-provisioned, eligible servers (v0.2.1). Deliberately smaller than `internal/provisioning`: no template resolution, ports, or server creation.
- `internal/scheduler`: typed local daily/weekly restart schedules; it only decides when to call `servers.Service.Restart` and has no runtime or command execution authority. This is NOT the v0.6 cluster placement engine (`internal/placement`, added later in this list) - the two packages are unrelated and neither imports the other. Do not rename, merge, or confuse them.
- `internal/gameconfig`: persisted, versioned declarative per-game configuration adapters and safe format-specific edits.
- `internal/nodeidentity`: this installation's own durable identity (`NodeID`, display name), protocol version, and the fixed, reviewed capability list it actually implements. Transport-free.
- `internal/nodes`: the Remote Node Foundation domain (v0.5A). Pairing tokens and trusted machine callers for THIS node (used when another GameNode enrolls it as a controller), and the registry of remote nodes THIS installation has enrolled as a controller. Never reads or writes another GameNode's database.
- `internal/remote`: the narrow, typed Remote Node client (`remote.Client`: `Enroll`, `GetNodeInfo`, `GetHealth`, `GetCapabilities`) and endpoint validation. No generic `DoRequest(method, arbitraryURL, body)`.
- `internal/placement`: the v0.6 Cluster Scheduling foundation. A pure, deterministic placement DECISION engine (`Decide`) over an already-built candidate list (this node plus every enrolled Remote Node), and read-only candidate builders (`LocalCandidate` from `servers.Service`/`nodeidentity`, `RemoteCandidates` from the v0.5A Remote Node registry). It never mutates a server anywhere, never contacts a remote node itself, has zero import of `internal/runtime`/`internal/provisioning`/`internal/remote` (enforced by a static regression test), and is completely independent of `internal/scheduler` (see below - do not confuse the two). EXECUTING a decision (`POST /api/v1/cluster/placement/execute` in `internal/api/cluster.go`) is a separate concern layered on top in `internal/api`: it dispatches to the existing `provisioning.Service` (local target) or to a new machine-authenticated Node provisioning path (`internal/remote.Client.StartProvisioning`, remote target) - never Docker, never a second container-lifecycle implementation. See `docs/adr/0009-cluster-scheduling-decision-vs-execution.md` and its addendum `docs/adr/0010-container-placement-execution.md`.
- `internal/database`: SQLite open/migration runner.
- `migrations`: ordered embedded SQL migrations. The v0.4 migrations are `024_egg_container_runtime.sql` and `025_server_restart_schedules.sql` (typed local restart schedules); the current highest file is `026_remote_nodes.sql` (v0.5A Remote Node Foundation).
- `web`: React/TypeScript/Vite source and Node helper tests. `cmd/gamenode/webassets` is generated production output embedded by Go. `web/src/tenants.tsx`/`tenants-helpers.ts` hold the Tenant admin UI (list/create/detail with Overview/Servers/Members/Access tabs) and the shared `useCreatableTenants()`/`resolveTenantSelection()` helpers reused by the Custom/Adopt server form and the Game Library provisioning wizard. `web/src/nodes.tsx`/`nodes-helpers.ts` hold the read-only Remote Nodes UI (list/detail/pairing/enrollment). There is still no router, only existing component-state navigation.
- `templates`: repository-owned Official Game Library manifest, templates, adapters, fixtures, and contribution rules.
- `docs`: architecture, security, runtime, API, development, CI, and ADR details.
- `server-test` and root `tmp-*` artifacts: local acceptance/reference material, not product architecture or proof of a fresh test run.

## 4. Core Architecture Rules

- `internal/api` owns transport concerns, not domain policy. Put validation and orchestration in transport-independent services where practical.
- Services must not depend on HTTP or WebSocket types merely for convenience. Runtime, console, filesystem, templates, SteamCMD, provisioning, and audit have deliberate transport boundaries.
- Keep OS-specific process, metrics, atomic-replace, upload-commit, symlink/reparse behavior behind interfaces or `_windows.go`/`_linux.go`/`_nonwindows.go` files.
- `internal/runtime` knows nothing about HTTP, WebSockets, RBAC, audit actors, or template/Egg formats. The controlled installer interface receives only an already-normalized bounded plan from provisioning.
- `servers.Service` is the authority for ordinary server lifecycle. Do not launch managed game processes directly from an API or provisioning handler.
- Templates and provisioning must end by creating an ordinary `servers.Server` with runtime type `native` or `container`. Do not create a second "Egg runtime" or template-specific lifecycle. Container Egg install jobs may use a short-lived installer container, but the registered server remains an ordinary `servers.Server` owned by `servers.Service`.
- Preserve direct `Executable` plus `Arguments[]` launching. A compatibility request is not permission to regress to shell-by-default.
- Managed game configuration may extend the launch only through `servers.Service`'s optional `LaunchResolver`, called immediately before `Runtime.Start`. Bindings are a closed compiled whitelist; never add an expression language, an index-based argument edit, or a user-supplied argument name, and never persist a resolved launch.

## 5. Runtime Invariants

- Persist and launch a structured executable and argument array. Never join them into a shell command. `WorkingDirectory` is an existing absolute server root; a relative executable must resolve inside it. An explicitly configured absolute executable is supported for custom/adopted applications.
- A managed process identity is PID plus OS start identity (`StartKey` internally), not PID alone. Status, metrics, stop, and kill must verify both to avoid PID-reuse bugs.
- A stale process, wait goroutine, console session, auto-restart generation, or finalizer must never overwrite a newer instance's state. Preserve identity comparisons and expected session/instance IDs.
- `servers.Service.finalizeInstance` is the exactly-once, centralized exit cleanup path (`sync.Once`). It alone closes the live session, persists final exit/crash state for the matching identity, updates monitoring, and schedules eligible auto-restart.
- Restarting GameNode must not terminate a surviving game process. Startup rediscovery verifies persisted identity; a survivor becomes `running` with console detached.
- Never claim console reattachment after rediscovery. GameNode cannot recover the original pipes/stdin.
- Manual `Stop` cancels pending auto-restart and uses the configured bounded stop method. `terminate` delegates to the native runtime and force-kills after timeout; `stdin_command` requires the matching attached session and force-kills after its timeout. `console_interrupt` (Windows only, compiled) sends a targeted `CTRL_BREAK_EVENT` to the process's own console process group and force-kills after its timeout; it requires the matching attached in-memory instance and falls back to `terminate` for a rediscovered/detached identity instead of claiming a graceful interrupt it cannot deliver. `Kill` is immediate. `Restart` serializes stop/finalization followed by the normal start path.
- `console_interrupt` delivery re-execs the same compiled GameNode binary as a disposable, single-purpose helper (`internal/runtime.RunConsoleSignalHelper`, gated by `main()` being the first call) that attaches to the target's console, generates the event, and exits. This exists because `GenerateConsoleCtrlEvent` requires the caller to share the target's console, which the long-running GameNode process may not have (Windows service/no console) and must not risk losing by attaching/detaching its own console in place. Do not add a second persistent service, an external script/shell helper, or a template-declared signal number/control character for this.
- A user-initiated stop/restart is not a crash. Non-zero unexpected exit becomes `crashed`; only that path can schedule auto-restart. Manual lifecycle actions cancel pending auto-restart. Delayed auto-restart is generation-guarded, rate-limited, and calls the same start orchestration including port preflight.
- A failed port preflight or process start is not a process crash and must not recursively schedule auto-restart.

See `docs/runtime.md` and ADR `docs/adr/0003-native-runtime-process-identity.md`.

### Egg Container Runtime invariants (v0.4)

- Native and Container compatibility are separate. Container provisioning requires an explicit `runtime_type: container`, a declared image, the administrator registry allowlist, an available Engine, and an explicit Pull; it never falls back to host execution.
- Egg installation scripts run only in a short-lived unprivileged installer container created through the Docker Engine API. The only persistent mount is the validated server root at `/home/container`; no daemon socket, host network/PID/IPC, devices, capabilities, arbitrary mounts, or registry credentials are allowed.
- Installer resources, timeout, cancellation, output, and cleanup are bounded. A failed install may leave owned files, but never creates a normal server row before validation and transactional registration. Unsafe recursive cleanup is not automatic.
- Startup expansion uses only declared normalized variables and `SERVER_ROOT`; host environment expansion, arbitrary engine flags, generic regex/eval/script configuration, and raw Egg JSON are forbidden. Existing servers persist the normalized provenance/image/digest/startup/sensitivity/ports/resources/config snapshot and are not silently migrated by catalog changes.

See `docs/architecture.md`, `docs/security.md`, and ADR `docs/adr/0008-egg-container-execution.md`.

## 6. Console Invariants

- Console state is in memory and per process instance. Current limits are 1,000 history events, subscriber queues of 128 events, 16 KiB input, and 64 KiB line-reader buffers.
- Publishing process output must never wait for a slow WebSocket client. Subscriber sends are non-blocking; a full subscriber queue is closed and removed.
- `Console.View` and `Console.Send` are independent permissions. View does not grant input and Send does not grant view.
- The WebSocket handshake checks `Console.View` and same origin. Existing code re-evaluates `Console.Send` for every input message and `Console.View` before every outbound event; preserve this live revocation behavior.
- Rediscovered processes are explicitly detached. Do not synthesize sessions, fake history, reopen stdin, or advertise reattachment.
- Do not audit console output or input contents. Console-input audit records only bounded metadata such as byte count and controlled outcome.

See `internal/console`, `internal/api/console.go`, and ADR `docs/adr/0004-console-io-lifecycle.md`.

## 7. Filesystem Security

- The server's configured working directory is the authoritative file root. RBAC authorization and sandbox validation are separate mandatory gates.
- Every client path is server-root-relative. Central logic normalizes separators, rejects NULs, traversal, absolute paths, drive-qualified paths, UNC/rooted forms, and any resolved target outside the canonical root.
- Canonicalize the root and existing targets. On Linux, symlinks may be exposed only when the resolved target stays inside the root; mutations operate carefully on the link itself where intended. On Windows, reparse points/junctions are conservatively rejected, including path components.
- All server-file endpoints and new game-file editing features must go through `internal/filesystem` or an equally strict central service built on the same invariant. Do not use ad-hoc `os.ReadFile`, `os.WriteFile`, `os.RemoveAll`, or path joining on user-supplied server-file paths in API code.
- Preserve bounded text reads/writes, directory entry limits, upload limits, regular-file checks, safe temporary files, and atomic commit/replace semantics.
- Recursive deletion must never be widened beyond the validated server-root target.

See `docs/security.md`, `docs/api.md`, ADR `docs/adr/0005-filesystem-sandbox.md`, and filesystem platform tests.

## 8. RBAC Rules

- The backend is authoritative. UI capability checks hide or disable affordances only.
- RBAC is allow-only. There are no deny rules or implicit permission hierarchies. A `*.Manage` permission does **not** imply `*.View`; `Files.Edit` does not imply any other Files permission.
- RBAC scopes are `global`, `tenant`, and `server`. For a permission evaluated against a specific server, it is effective via an enabled admin bypass, a direct/group global assignment, a direct/group tenant assignment for that server's own tenant, or a direct/group server assignment for that exact server - never a tenant assignment for a different tenant. Roles stay scope-neutral; only an assignment carries a scope. Tenant membership (`internal/tenants`) alone never grants a permission. Permissions classified by `rbac.GlobalOnly` never become effective from a tenant or server assignment; `rbac.AllowedScopes` is the single source of truth for which scopes a given permission accepts, consumed by both the evaluator and `ServerAssignable`/`TenantAssignable`'s shared whole-role-suitability check.
- A disabled user is denied before the enabled-admin bypass. An active administrator bypasses the normal evaluator.
- Current groups are Server (`View/Create/Edit/Delete/Start/Stop/Restart/Kill/Update`), Console (`View/Send`), Files (`View/Edit/Upload/Download/Delete/Rename`), Ports (`View/Manage`), identity (`Users`, `Groups`, `Roles`: `View/Manage`), platform (`Settings.View/Manage`), Templates (`View/Manage`), `Monitoring.View`, `Audit.View`, Tenants (`View/Manage`, administering tenant entities themselves - never resources inside one), Node (`View/Manage`, administering this installation's Remote Node registry/pairing - never a remote node's own resources, which stay behind that node's own RBAC), and Cluster (`View/Schedule`, v0.6: viewing placement candidates/capacity and computing a placement decision for a tenant - `Cluster.Schedule` alone never causes a server mutation; `POST /api/v1/cluster/placement/execute` additionally requires tenant-scoped `Server.Create`, the same permission normal provisioning already needs, before it dispatches anything). The code catalog is definitive.
- Global-only currently includes all Users/Groups/Roles permissions, Settings, Templates, `Audit.View`, `Tenants.View`/`Tenants.Manage`, and `Node.View`/`Node.Manage`. `Server.Create` is the one deliberate exception among server-family permissions: it allows `global` and `tenant` but never `server` scope, since a server does not exist yet when `Server.Create` is evaluated. `Cluster.View`/`Cluster.Schedule` (v0.6) follow the same `global`+`tenant`-only rule as `Server.Create`, for the same reason: a placement decision is evaluated before any server exists.
- `Node.View`/`Node.Manage` govern only this installation's controller-facing Remote Node registry API (`/api/v1/remote-nodes*`, `/api/v1/node/pairing-tokens`) and its UI. They are a separate, browser/RBAC/CSRF-gated trust domain from the machine-authenticated Node-facing API (`/api/v1/node/info|health|capabilities|enroll`), which never checks RBAC or CSRF and instead validates a bearer machine credential (see `docs/adr/0007-remote-node-foundation.md`).
- Template browsing/provisionability and provisioning currently require global `Templates.View`; import/delete/refresh requires `Templates.Manage`. Starting or inspecting a provisioning job additionally requires global `Server.Create`. Jobs are owner-visible/cancellable, with active admins allowed to act for the owner.
- `Server.Update` (v0.2.1, manual SteamCMD server update) is a normal server-family permission with no implicit inheritance from `Server.Edit`, `Server.Start`, or `Templates.Manage` - updating an already-provisioned server is a server operation, not catalog administration. Server-update job read/cancel follow the same owner-or-admin rule as provisioning jobs.
- Update the API product capability list and coverage tests whenever the permission catalog changes.

## 9. Audit Rules

- Use the central action/resource catalog in `internal/audit`; do not invent near-duplicate action strings in handlers.
- Relevant mutations should emit one semantic result after their business outcome is known. Internal steps of a manual restart must not produce duplicate user-originated lifecycle events. Provisioning has separate start and terminal events.
- Audit is append-only through the product API and best effort: an audit storage failure is logged but must not roll back an already successful primary mutation.
- Metadata is controlled, schema-like, bounded (currently 4 KiB), and sanitized. Never record passwords, cookies, session/CSRF tokens, raw credentials, sensitive environment values, full arguments when they may contain secrets, file/upload contents, console input/output, raw Egg JSON/scripts, or SteamCMD output.
- Failure codes/summaries must be controlled and non-sensitive; do not pass raw external output or arbitrary request bodies into audit.
- Denial auditing is not globally complete. Some paths (notably console input permission failure) record a denial, but do not claim or locally improvise comprehensive denial auditing without an architecture decision.

## 10. Templates and Game Library Model

- Official Template schema v2 is the contributor contract; schema v1 remains
  readable for cached/backward-compatible data. Templates are data, never code.
  Installer/resolver values are whitelisted, platform support is explicit,
  selected paths are server-root-relative and sandboxed, and resolution yields
  only executable + arguments/environment/working-directory/stop data.
- Schema v2 expected files are validated after installation and before server
  registration. Requirements distinguish enforceable host facts from
  informational hints. Existing servers pin their resolved launch, ports,
  adapters, and template provenance/version; catalog updates never mutate them.

- Normalized `templates.Template` is the runtime-independent source of truth. Eggs are an untrusted import format, never a runtime format.
- Current source values are `official`, `builtin`, and `pelican-pterodactyl` (shown as imported in the UI). Official and built-in entries are read-only; imported rows live in SQLite.
- Egg ingestion is bounded, strict, and conservative. It stores normalized fields and a provenance hash, not raw Egg JSON. Native and Container compatibility are separate. Container plans retain only strict image refs, bounded scripts/startup, declared variables, compiled config operations, and resource defaults; Container paths collapse only to semantic `server_root`.
- A safe direct-process prefix may be analyzed; shell tails/operators are discarded and reported as compatibility findings. Container Egg scripts are never executed on the host: an explicit Container provisioning request may run the normalized plan only in the controlled unprivileged installer boundary.
- Official repository data is still untrusted network input and passes strict schema and safety validation. It has no validation bypass because it is "official."
- Template compatibility (`compatible`, `partially_compatible`, `unsupported`) is distinct from host-specific provisionability. Partial compatibility is not permission to ignore a missing safe installer or launch.
- Existing servers persist their resolved launch, variable provenance/sensitivity, ports, and adapter snapshots. A catalog version update affects future creates only; never silently mutate existing servers.

## 11. SteamCMD Rules

- Managed SteamCMD lives at `<data>/tools/steamcmd`; provisioned roots live at `<data>/servers/<directory>`.
- Bootstrap sources are fixed in code to Valve's HTTPS archives for Windows and Linux. Do not accept a URL, mirror, archive path, executable path, or authentication endpoint from template/user input.
- Downloads and ZIP/TAR extraction are bounded and path-safe. Reject absolute paths, traversal, links/special entries, excessive entry counts, and excessive extracted size. Keep serialized/atomic bootstrap behavior.
- Invoke with `exec.CommandContext(executable, arguments...)`; never a shell. Arguments are constructed from reviewed fields: fixed install directory, catalog-owned positive App ID, anonymous login, optional constrained beta branch, validation boolean, and `+quit`. Do not add arbitrary flags.
- Normal users never submit a free-form App ID. Current login support is anonymous only; credentials, Steam Guard, auth variables, and beta passwords are rejected.
- SteamCMD's own normal client self-update may occur during invocation, but GameNode has no automatic server-update, update-on-start, or scheduling engine. Do not represent initial provisioning as ongoing update management.
- SteamCMD supports initial provisioning and explicit manual updates of eligible existing SteamCMD-managed servers (`internal/serverupdates`, v0.2.1). Automatic, scheduled, and update-on-start SteamCMD server updates remain unsupported. A manual update reuses `steamcmd.Manager.Install` unchanged against the server's existing root, using only App ID/validate/login-mode/template provenance persisted once at provisioning time in `server_steamcmd_provisioning` (never the live catalog); it never migrates the server's pinned template version, executable, arguments, ports, stop behavior, or configuration adapter snapshots. See §12 and §15 below and `docs/architecture.md`'s "Manual SteamCMD server updates architecture" section.

## 12. Provisioning Semantics

- Flow: validate template/request and reserve target; ensure managed SteamCMD; install with structured args; resolve and verify host launch inside the root; transactionally create one normal native GameNode server plus metadata/ports/adapters.
- A server database row is created only after successful installation and final launch validation. Install failure/cancellation must not leave a ghost server record.
- Filesystem installation cannot be atomic with SQLite. A failure after files are created may leave them; report `files_may_remain`. Never recursively delete a possibly unowned target as automatic recovery.
- Jobs persist safe status/phase, actor/template identity, relative directory name, outcome, and optional server ID—not raw output, variable values, credentials, or absolute host paths.
- Terminal state is exactly once. Cancellation stops the process and gates final server creation; cancellation is rejected once transactional finalization begins. Active jobs interrupted by GameNode restart become failed/interrupted and are not resumed.
- Target reservations prevent concurrent work on the same directory. The SteamCMD manager serializes bootstrap/update-sensitive access while different target jobs may otherwise proceed independently.
- Manual SteamCMD updates (v0.2.1, `internal/serverupdates`) are a separate, much smaller domain: they never resolve a template, never touch ports/config adapters, and never create a server row. They reuse `servers.Service.BeginUpdate`'s reservation (checked by Start/Restart/Delete) instead of a second target-reservation map, and reuse `steamcmd.Manager.Install` unchanged against the server's already-persisted root.

## 13. Official Game Library

- `templates/catalog.json` is manifest schema v1 and lists relative JSON files below `templates/`. The fixed source is `https://raw.githubusercontent.com/Nikolai-Ahlhelm/GameNode/main/templates/`; there is no configurable source, GitHub API discovery, token, hash, or signature in v1.
- The catalog, each template, and same-directory adapter are independently bounded and validated. Relative files cannot be URLs, absolute/drive/UNC paths, traversal, or unlisted paths.
- `<data>/templates/cache` is a sanitized last-good cache. It loads during catalog construction. Remote refresh is on library use/manual refresh, not a startup dependency; refresh failure preserves cached official entries and imported/built-in availability, marks offline state, and must not break GameNode startup.
- Current repository catalog entries are Minecraft NeoForge 2.0.0 (adopt existing, Windows/Linux), 7 Days to Die 2.0.0 (SteamCMD, Windows/Linux), Project Zomboid 2.0.0 (SteamCMD, Windows only), Palworld 1.1.0 (SteamCMD, Windows only), Satisfactory 1.1.0 (SteamCMD, Windows only, `console_interrupt` stop method), Eco 1.0.0 (SteamCMD, Windows/Linux), and Valheim 1.1.0 (SteamCMD, Windows only, schema-v2 `managed-launch` adapter).
- To add/update an official template: follow `templates/README.md`; add/update reviewed JSON under the correct game directory; keep IDs stable; bump template version for behavior/default/port changes; update `catalog.json` in the same change; validate backend and frontend helpers; use an opt-in real provision/start/stop smoke when practical.
- Configuration adapter schema v1 (file formats) and v2 (adds `managed-launch` bindings) are both supported; v1 adapters must keep working unchanged. A `managed-launch` field key must match a declared template variable and must not also appear as a base-launch placeholder, so a setting has exactly one source of truth.
- Catalog schema v1 and Official Template schema v2 must match the code constants; Template schema v1 remains readable for cached/backward-compatible data. `minimum_gamenode_version` is validated and enforced as an unsupported compatibility finding for older release builds; development versions are intentionally treated as current enough.

## 14. Frontend Rules

- Frontend code is React + TypeScript + Vite under `web`; production assets are generated into and embedded from `cmd/gamenode/webassets`.
- Navigation is currently component state in `DashboardModern`, not a URL router. Server detail tabs are local state. Do not introduce or replace the routing/framework model merely for style.
- The UI is capability-aware, but the backend remains the security boundary. Reload authoritative server detail/capabilities after create, update, and lifecycle changes.
- Preserve the established dark infrastructure UI, desktop-first responsive behavior, and shared `PageHeader`, `SectionHeader`, `LoadingState`, `EmptyState`, and error/notice patterns.
- Every asynchronous surface needs honest loading, error, and empty states. Never fabricate metrics, health, console attachment, progress, or sample data.
- Reuse existing APIs and domain data. Do not add a backend endpoint or persistence field solely to make a graph look richer unless the milestone requires it.
- Theme (dark/light/system), sidebar-collapsed, and wallpaper are personal browser-local preferences stored only in `localStorage` via `web/src/theme.ts` (`gamenode:ui-preferences`) - never instance-wide `internal/settings` state. Design tokens live centrally in `web/src/styles.css`'s `:root`/`:root[data-theme="light"]` blocks; do not hardcode a new theme-dependent color in a component or per-page CSS file. A wallpaper image is processed and validated entirely client-side (`web/src/wallpaper.ts`: PNG/JPEG/WebP only, decoded and re-encoded through canvas, never SVG, never a remote URL) and never uploaded to the backend. See `docs/architecture.md`'s "UI theme, preferences, and wallpaper foundation" section.

## 15. Database and Migration Rules

- SQLite via `modernc.org/sqlite` is the only database. Foreign keys and a busy timeout are enabled at open.
- Migrations are committed SQL, embedded by `migrations_embed.go`, sorted lexically, and applied once inside individual transactions. Inspect `migrations/` before choosing a number; current highest is `023_server_update_metadata.sql`.
- `internal/database.Migrate` applies every pending migration with foreign key enforcement disabled on one dedicated connection, then verifies `PRAGMA foreign_key_check` before re-enabling it. This exists so a table rebuild (see 020, which gives `servers` a mandatory `tenant_id`) can `DROP`/rebuild a table that other live tables still reference by foreign key without SQLite's implicit cascading `DELETE` on `DROP TABLE` destroying that unrelated data. A migration that rebuilds a referenced table should rely on this rather than reintroducing its own pragma toggling.
- Never edit an already applied migration. Add the next zero-padded migration and update tests/queries/models together.
- Verify both a fresh database and upgrade from the previous schema. Migration SQL must be deterministic and must not depend on local paths, network, clock-based data decisions, or unordered input.
- Store/parse timestamps as UTC RFC3339Nano unless an existing schema contract says otherwise.

## 16. Testing Expectations

For backend changes, run from repository root:

```text
gofmt -w <changed .go files>
go vet ./...
go test ./...
go build ./...
```

Use `go test -race ./...` where the native CI/toolchain supports CGO and a C compiler. Do not run `gofmt -w` blindly over generated/vendor-like artifacts; format changed Go source.

For frontend changes, run from `web`:

```text
npm ci
npm run check
npm run test:helpers
npm run build
```

Production/release verification must build the frontend before Go so embedded assets are fresh, then build Windows amd64 and Linux amd64 artifacts as described in `docs/development.md` and the workflows. Cross-build success is not native runtime acceptance.

Tests and root harnesses contain marker strings such as `E2E_*`; a historical marker/file is not evidence. Claim an E2E or native acceptance marker only after a fresh execution in the current turn/environment, and report skips/failures exactly.

Documentation-only changes normally require no code suite. Always run `git diff --check`; run targeted doc/link/schema checks when applicable.

## 17. CI and Release

- `.github/workflows/ci.yml` runs for PRs targeting `main`, pushes to `main`, and manual dispatch. It runs Linux formatting/vet/test/build, Windows test/build, Linux amd64 race tests with CGO, and frontend install/check/helper tests/build.
- CI then rebuilds the embedded frontend and produces Windows amd64 and CGO-disabled Linux amd64 development artifacts. Successful `main` push artifacts are unsigned and retained for 14 days.
- `.github/workflows/release.yml` runs on pushed `v*` tags but explicitly validates semantic-version syntax. It repeats Linux Go/frontend/race and Windows Go verification before packaging.
- Release packaging builds the frontend before each Go binary, injects version/commit/UTC build time, publishes `gamenode-windows-amd64.exe`, `gamenode-linux-amd64`, and `SHA256SUMS.txt`, and only publishes after both artifacts succeed.
- Normal CI has read-only contents permission; release publishing has contents write permission and uses `GITHUB_TOKEN`.

## 18. Environment and Platform Notes

- Windows symbolic-link creation may require privileges; the affected tests legitimately skip when the OS denies link creation. Junction/reparse-point escape tests are distinct and must remain covered.
- `go test -race` requires CGO and a working C compiler (for example GCC). A Windows machine without them cannot prove the race suite; CI's Linux race job is the current baseline.
- Native runtime, metrics, console process behavior, atomic replacement, upload commit, symlink, and reparse behavior differ legitimately by OS and have platform-specific tests.
- Do not weaken or skip repository security logic, alter production behavior, vendor fake binaries, or add environment-specific hacks to make a constrained local machine green. Report the limitation and rely on the correct native/CI job.

## 19. Security Review Checklist

Before completing a security-relevant change, ask:

- Did this introduce a shell, script hook, command string, or executable configuration language?
- Can a user/template select an arbitrary URL, App ID, SteamCMD flag, host path, or credential flow?
- Can any decoded, normalized, symlinked, junction, reparse, move, upload, archive, or adapter path escape its root?
- Can secrets, raw environment values, file contents, console input/output, credentials, or external tool output leak through API, logs, audit, diagnostics, or support bundles?
- Is every backend action protected by the exact independent RBAC permission and correct global/server scope? Is disabled-user/admin behavior preserved?
- Does every authenticated mutation have same-origin/CSRF enforcement, including WebSocket origin checks where applicable?
- Can a stale PID/session/finalizer/restart/job overwrite a newer generation or finalize twice?
- Is any history, request body, archive, output buffer, subscriber queue, job field, or query result unbounded? Can a slow client block process output?
- Is audit metadata narrowly controlled, bounded, exactly once semantically, and sanitized on failure?
- Did the support-bundle five-file whitelist or 10 MiB cap change unintentionally?
- Can an imported or official template become arbitrary code execution through launch, resolver, adapter, installer, or update behavior?
- Remote Node specific: does a browser session or CSRF token ever authenticate a Node-facing (`/api/v1/node/*`) call, or does a machine credential ever skip RBAC/CSRF on a controller-facing (`/api/v1/remote-nodes*`) call? Is a pairing token/machine credential ever logged, audited, or returned more than once? Does `internal/remote.Client` still refuse `InsecureSkipVerify`, cap response size, apply a timeout, and refuse to follow a cross-host redirect? Does connection/health state to a remote node ever get treated as authoritative over that node's own server/runtime state? See `docs/adr/0007-remote-node-foundation.md`.

## 20. Change Discipline

- Read the implementation and nearby tests before designing a replacement. Use the working tree as current context; do not overwrite unrelated user changes.
- Prefer small coherent changes and explicit interfaces at real boundaries. Do not rewrite an entire subsystem for naming, style, or hypothetical reuse.
- Preserve existing tests and add a regression test for every fixed bug or newly protected invariant.
- Never weaken path, process-identity, RBAC, CSRF, archive, template, secret-redaction, or boundedness rules for compatibility. Surface the incompatibility instead.
- Keep generated frontend assets synchronized only when a frontend production build is part of the requested change; review hashed asset churn carefully.
- Do not implement roadmap work automatically. If a requirement conflicts with these invariants, stop and explain the conflict and migration/security consequences explicitly.

## 21. Documentation Sources of Truth

- `PROJECT_PLAN.md`: product constraints, original v0.1 decisions, and appended v0.2 milestone status. Older milestone language is historical, not an instruction to reimplement or stop completed work.
- `README.md`: current operator-facing capabilities, setup, high-level security, and limitations.
- `docs/architecture.md`: component boundaries and completed architecture slices.
- `docs/security.md`: threat model and security constraints.
- `docs/api.md`: current HTTP/WebSocket contracts.
- `docs/runtime.md`: native lifecycle, identity, rediscovery, console, and OS limitations.
- `docs/development.md` and `docs/ci.md`: exact local/CI/release verification.
- `templates/README.md`: official catalog/template/adapter contribution contract.
- `docs/adr`: durable decisions behind SQLite migrations, cookie/CSRF, process identity, console lifecycle, filesystem sandboxing, tenant isolation, and Remote Node identity/trust (`0007-remote-node-foundation.md`).

Code and tests are the final implementation truth. Keep docs synchronized when behavior changes. Do not let stale roadmap wording override shipped architecture, and do not treat an uncommitted working-tree feature as released merely because its docs are present.

## 22. Current Project Status

- Repository history contains tags `v0.1.0` and `v0.2.0`; the current checked-out commit is tagged `v0.2.0`. v0.1 delivered the single-node native-management foundation. v0.2.1 is a small intermediate release, not a new numbered milestone: manual, operator-triggered SteamCMD server updates for eligible already-provisioned servers (see `PROJECT_PLAN.md`'s "v0.2.1 — SteamCMD Server Updates status" section).
- Managed launch/environment configuration bindings (adapter schema v2) are implemented with Valheim as the reference game. A `launch-secret` value is inserted into the child argv only at start; it is excluded from APIs, audit, logs, diagnostics, support bundles, job state, and the persisted registration snapshot. Local OS process inspection of arguments remains an unavoidable game-imposed limitation.
- A provisioning job carrying managed secret values persists no registration snapshot and is not registration-recoverable. Do not "fix" this by storing secrets in job state or by retrying from a redacted snapshot; that would create a server with silently missing configuration.
- The launch resolver reads only the per-server `server_config_adapters` snapshot and `server_config_values`. `internal/gameconfig` has no catalog dependency; never let runtime resolution consult the live Official catalog.
- The current repository/worktree implements the v0.4 Container-backed Egg runtime alongside the v0.3 Container runtime, plus the v0.2 Egg import foundation, normalized template persistence, global template RBAC/audit/UI, managed fixed-source anonymous SteamCMD provisioning, persisted cancellable jobs, the remote/cache-backed Official Game Library, NeoForge adoption, official 7 Days to Die and Project Zomboid templates, template provenance, and versioned declarative game-configuration adapters.
- Some of the latest catalog/adapter/provisioning work is present as uncommitted working-tree changes. Treat it as active current code for edits and reviews, but do not call it part of a published tag without checking the committed/tagged tree.
- v0.6 (`internal/placement`) adds a deterministic, tenant-isolated, RBAC/audit-gated Cluster Scheduling placement DECISION engine across this node and every enrolled Remote Node, plus its `GET /api/v1/cluster/capacity`/`POST /api/v1/cluster/placement` API - this part deliberately never mutates any server, local or remote. A second phase adds EXECUTION: `POST /api/v1/cluster/placement/execute` recomputes the decision server-side and dispatches the caller's typed provisioning fields to the selected node - locally through the unmodified `provisioning.Service`, or remotely through a new narrow, typed, machine-authenticated Node provisioning path (`POST /api/v1/node/provisioning`, proxied by `POST /api/v1/remote-nodes/{id}/provisioning`). This is deliberately not full v0.5B remote server management: there is still no way to start/stop/restart/edit/delete an *already-existing* remote server, only to create one via provisioning, and only through this one typed path. See `docs/adr/0009-cluster-scheduling-decision-vs-execution.md` for the original Decision vs. Execution boundary and `docs/adr/0010-container-placement-execution.md` for how execution was added without reopening it.
- v0.4 Container-backed Egg Runtime and v0.5A Remote Node Foundation were developed concurrently on separate branches/worktrees and have since been integrated onto a common base. v0.5A (`internal/nodeidentity`, `internal/nodes`, `internal/remote`, the `/api/v1/node/*` and `/api/v1/remote-nodes*` API, migration `026_remote_nodes.sql`, and the read-only Nodes UI) does not depend on v0.4 and did not originally advertise an Egg Runtime capability. Now that v0.4 is integrated, `internal/nodeidentity`'s capability list includes a truthful `egg_container_runtime` entry (see section 5 above); `internal/nodes`/`internal/remote` still do not import or understand Egg internals. v0.5A remains identity, pairing/trust, a read-only Node API, and registry/health foundation only - remote Server Create/Edit/Start/Stop/Restart/Kill/Console/Files/provisioning remain unimplemented until v0.5B.
- Known boundaries: no host-side generic Egg scripts, no registry credentials, no credentialed Steam login/Steam Guard; no automatic, scheduled, or update-on-start server updates (manual updates only, v0.2.1); no template migration when updating a server; no automatic resumption after interrupted provisioning (owner-initiated installed-target registration recovery is bounded); no encrypted-at-rest environment secrets; no community/configurable catalog or catalog signatures; no automatic NeoForge/Minecraft install or EULA mutation; Project Zomboid official provisioning is Windows-only; rediscovered consoles stay detached; port probes are best effort with a bind-time race.
- Historical Windows acceptance statements in README/docs are dated records. Linux native smoke and any new real game provision/start/stop acceptance must be rerun before being claimed for a new change.

## 23. Agent Completion Format

End implementation turns with a concise, evidence-based report containing, as applicable:

- scope implemented and explicit final status;
- architecture/security decisions and preserved invariants;
- files changed;
- migrations added (or "none");
- security review result;
- backend tests and race result;
- frontend checks/build and visual verification;
- Windows/Linux build or native acceptance status;
- known limitations, skips, and remaining contradictions.

Do not write `ACCEPTED`, claim an E2E marker, or imply runtime acceptance unless that exact flow was freshly executed and observed.
