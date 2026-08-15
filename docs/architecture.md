# Architecture

GameNode is a single Go process. The transport layer is `internal/api`; authentication and setup logic live in `internal/auth`; local-user and group administration lives in `internal/identity`; server application logic and persistence live in `internal/servers`; OS-specific native process implementations are isolated in `internal/runtime`; server-root filesystem policy and read operations live in `internal/filesystem`; SQLite access and migrations live in `internal/database`. The React UI is a separate development application and is built into `cmd/gamenode/webassets` for releases.

The embedded production UI is a capability-aware affordance layer only. After every server create, update, or lifecycle response it reloads the server detail resource so the server-scoped capability list remains authoritative for the displayed lifecycle, console, file, port, monitoring, and configuration controls.

The identity migration extends the existing `users` table and adds groups plus a cascading membership join table. Groups are stable membership containers; role assignments attached to them participate in RBAC evaluation.

The RBAC core provides a static permission catalog, allow-only roles, user/group role assignments, and global or server scopes in `internal/rbac`. Disabled users are denied before the administrator bypass; enabled administrators bypass the evaluator. `internal/api` centrally applies the evaluator to Server, Console, Files, and existing Users/Groups/Roles management endpoints. Platform permissions use only global scope; server monitoring uses server-scoped `Monitoring.View`. The backend remains authoritative and the UI only uses capabilities for affordances.

Roles are scope-neutral definitions; assignments carry scope. Global grants of a server-capable permission apply to every server, while direct or group server grants apply only to the assigned server. Server assignment validation is whole-role and explicit: empty roles and roles containing any global-only permission are not server-assignable, and permission replacement cannot turn a currently server-assigned role into a mixed-scope role. This avoids silently dropping part of a role at evaluation time.

`internal/monitoring` owns the bounded in-memory sampler and health calculation. It depends only on identity-verified metrics from `internal/runtime`, while `internal/servers` remains lifecycle authority and persists compact exit/crash/restart state. A finalizer only changes monitoring state when its full process identity still matches.

The same server service owns the opt-in auto-restart policy. Its central finalizer persists a crash before scheduling one cancellable delayed restart; the delayed action calls normal start orchestration and is guarded by a per-server pending generation.

Audit 6D.1 provides `audit.Service` persistence: the application layer can record Actor, Action, Resource, Server, Result, bounded Metadata, and Error Summary into SQLite. It is append-only through the product API, lists newest first (`timestamp DESC`, then `id DESC`), and prepares bounded filtering/pagination. The service knows neither HTTP, Runtime, ConsoleManager, nor filesystem implementation.

In 6D.2a, the API composition layer adds a small best-effort audit helper for authentication. A normal credential login reaches authentication first; once its outcome and, on success, the session creation are known, the helper records `auth.login`. Successful logout is recorded after session invalidation succeeds while the authenticated actor snapshot is still available.

In 6D.2b, the same API-layer helper records the final success or failure of server create, update, delete, start, stop, restart, and kill requests, as well as port create, update, and delete requests. This keeps the HTTP actor and direct remote-address semantics out of `servers.Service`, `ports.Service`, Runtime, and ConsoleManager. A manual restart is recorded once as `server.restart`; its internal stop and subsequent normal start do not generate additional user-originated server events. Auto-restart system events are not yet instrumented. Files, console, identity, and RBAC action instrumentation remain deferred.

In 6D.2c, file mutation routes perform their filesystem operation before recording one result through the API audit helper. The helper receives only the already validated sandbox-relative path and controlled metadata; `internal/filesystem` remains unaware of audit, HTTP actors, and remote addresses. Console WebSocket input is audited after its per-message permission check and input attempt, using the authenticated connection actor, original direct remote address, server ID, and payload byte count only. ConsoleManager remains transport- and audit-agnostic. File reads/listing and console output are not audited.

In 6D.2d, user, group, membership, role, permission-replacement, and role-assignment mutations follow the same API mutation → business service → result → audit helper flow. Identity and RBAC services remain independent of HTTP actors. Assignment events record only subject ID/type and global or server scope; permission replacement records a bounded final permission summary. Password material, session data, membership lists, and evaluator internals are never passed to the helper.

The consolidated 6D.2 audit surface is Auth, server and port mutations, file mutations, Console input, and identity/RBAC mutations. Audit writes are best effort and append-only through the product API. Retention/export workers, global permission-denial audit, and auto-restart system events remain out of scope. Runtime, ConsoleManager, filesystem, ports, and the RBAC evaluator do not depend on audit or HTTP actor data.

6D.3 adds the read-only flow: Audit UI → `GET /api/v1/audit` → global `Audit.View` → `audit.Service.List` → SQLite. The API uses stored actor/resource snapshots and does not join current resources, so deleted targets remain visible.

Dashboard aggregation is read-only: the API filters visible servers by RBAC before passing existing monitoring snapshots and registered ports to `internal/dashboard`; audit recents are included only with global `Audit.View`.

`internal/settings` is a transport-independent typed settings service backed by SQLite. Its whitelist covers instance name/subtitle/favicon, monitoring interval/history, logging level, and bounded password minimum/maximum lengths. Favicon bytes are stored separately from the settings response and support bundle; only a boolean presence flag is exposed there. Compiled defaults are overlaid by YAML startup configuration where applicable, then by valid persisted SQLite overrides; unknown database keys are ignored while invalid known persisted values fail the settings load rather than being silently accepted. The API supplies global-only Settings.View/Settings.Manage authorization and records one best-effort `settings.update` event after each mutation. Monitoring construction still consumes its options at process startup and is marked restart-required; branding, logging, and password-policy changes are read live and apply immediately.

`internal/diagnostics` is a transport-independent read-only summary service. It reads safe Go runtime/build information, process uptime, OS/architecture, SQLite migration state, and existing monitoring/settings values. Release builds inject the version tag, commit SHA, and build time through linker flags; these safe values are exposed in the application summary. The API enforces global-only `Settings.View`; Diagnostics uses no HTTP dependency, shell commands, environment dump, path disclosure, or network enumeration.

`internal/support` creates a streaming ZIP support bundle through an `io.Writer`. The Settings UI sends `POST /api/v1/support/bundle`; API authentication, global-only `Settings.Manage`, and CSRF run before bounded generation, then the browser downloads the fixed ZIP. Its whitelist is exactly `manifest.json`, `diagnostics.json`, `settings.json`, `audit-recent.json`, and `servers.json`: a manifest, diagnostics, web-safe settings, at most 100 recent sanitized audit events, and safe server lifecycle/health summaries. Audit-read failure is tolerated with an empty audit entry and controlled manifest warning; settings and server failures fail generation. The API buffers the core-capped (10 MiB) bundle before committing the response, persists no temporary bundle, and writes the best-effort generation audit event only after successful generation. It contains no raw database, YAML configuration, environment, logs, server files, console data, paths, or executable configuration.

The port transport routes call `ports.Service`, which validates assignments, checks registry collisions, and performs best-effort temporary OS availability probes. `servers.Service.start` performs port preflight after lifecycle validation but before state mutation, process-instance/console creation, and `Runtime.Start`. Manual restart finalizes the old process before normal start/preflight; auto-restart follows finalizer, delay, and that same start path. Runtime, ConsoleManager, and HTTP transport do not implement collision logic themselves.

v0.1 provides server-root-scoped directory listing, bounded text reads, safe create/edit/move/delete operations, streaming upload/download transport through `internal/filesystem`, and a Files tab with a bounded Monaco text editor. Filesystem sandboxing stays independent from RBAC: RBAC authorizes an action while `internal/filesystem` validates every path. Archive browsing, cluster operation, and Docker remain out of scope.
# Template import architecture

The normalized Template contract is declarative data, never executable code.
Schema v2 keeps the v1 installer/launch foundation and adds typed Requirements,
expected installation artifacts, configuration-file metadata, UI grouping, and
port intent. Installer and resolver names are closed whitelists. Platform support
is explicit; paths remain server-root-relative; launch is always a normalized
executable plus argv/environment/working-directory/stop definition. Existing
servers pin resolved runtime data and template provenance/version, so catalog
updates affect future creates only. See `docs/templates.md`.

Pelican/Pterodactyl Eggs enter GameNode through a bounded parser and are converted before persistence:

```text
Egg JSON -> structural validation -> compatibility analysis
         -> GameNode Template -> native installer/launch plans
```

`internal/templates` is transport-independent. Its `Template` owns provenance metadata, an installer definition, an optional structured launch definition, typed variables, and deterministic compatibility findings. The SQLite store persists the normalized root plus ordered variable and finding rows. Only a SHA-256 provenance hash is retained from the original input; raw Egg JSON and install scripts are not the runtime source of truth and are not persisted.

The installer definition supports a detected SteamCMD plan (`app_id`, `validate`, `login_mode`, `platform`, beta branch/password variable references, and semantic `server_root`). Container paths such as `/mnt/server` and `/home/container` collapse to `server_root` semantics and never become host paths.

The launch analyzer tokenizes only a single direct process into `Executable` plus `Arguments[]`. A safe prefix before a shell operator may be retained with a partial-compatibility finding; the remaining shell tail is discarded. Runtime code remains independent of Eggs and continues to use the existing native server model.

# Native SteamCMD provisioning architecture

`internal/steamcmd` is transport-independent. It owns the fixed platform source, bounded HTTPS download, path-safe ZIP/TAR extraction, managed executable detection, serialized atomic bootstrap, structured command arguments, streaming output interface, cancellation, and exit status. Its managed directory is `<data>/tools/steamcmd`; Linux invokes the native `linux32/steamcmd` binary with a controlled library path, and Windows invokes `steamcmd.exe`. SteamCMD performs its normal signed client self-update during invocation; GameNode does not download replacement binaries from template input. One idempotent retry covers transient Steam client/app-metadata process failures; a second failure remains a normal failed provisioning job. No shell participates.

`internal/provisioning` coordinates a short persisted job record and in-memory execution state:

```
validate template/request -> reserve <data>/servers/<directory>
  -> ensure managed SteamCMD -> install with structured arguments
  -> expand direct launch fields -> transactionally create server + variable metadata
  -> release reservation and finalize exactly once
```

Actual variable values live only in the existing server environment record; `server_template_variables` retains template ID, source, version, key, and sensitivity metadata. A normal server row is inserted only after installation and final launch validation. Filesystem writes cannot participate in the SQLite transaction, so a failed final database insert can leave installed files; the job reports this explicitly and GameNode never performs unowned recursive cleanup.

Jobs persist safe phase, actor/template identity, directory name, result, and server ID, but not raw output, values, credentials, or an absolute host path. Active jobs are marked failed/interrupted during startup; v0.2 intentionally has no resume engine. In-memory cancellation terminates the SteamCMD process and gates final server creation. A per-target reservation allows different roots in parallel, while the SteamCMD manager serializes first bootstrap/update access.

Provisionability is narrower than general template compatibility: a partially compatible Egg may proceed only when its native SteamCMD plan, anonymous authentication, current-host launch, variables, and direct launch definition are sufficient. Unsupported templates, beta passwords, credentialed login, absent host launch definitions, and arbitrary flags are rejected before execution.

Compatibility is `compatible`, `partially_compatible`, or `unsupported`. Findings contain stable severity, component, code, and summary fields. Warnings yield partial compatibility and errors yield unsupported status, making the result deterministic and suitable for API/UI rendering and tests.

Official templates reuse the normalized template domain model but are loaded through `templates/catalog.json` from the fixed HTTPS GitHub Raw base for this repository's stable `main` branch. The client depends on the manifest rather than GitHub directory or API behavior. It validates the catalog, independently fetches and validates each relative template, isolates malformed entries, and exposes Official entries alongside imported DB-backed templates without persisting the remote catalog into template tables.

`<data>/templates/cache` stores the last successful sanitized manifest, validated templates, and fetch timestamp with same-directory temporary-file replacement. Startup reads cache only and never waits for GitHub. Refresh is serialized and updates in-memory state only after cache publication; failure preserves last-good memory/disk state. A cache hit is explicitly marked cached/offline, while no network and no valid cache yields an empty Official result without affecting other subsystems.

The NeoForge Official template uses a named resolver rather than a version-specific library path. The resolver reads only bounded files beneath the selected server root, recognizes the generated direct-Java launcher shape, selects the host argfile, validates referenced files, and returns a structured launch definition. No NeoForge downloader exists yet.

Official SteamCMD templates use the same template and provisioning services. `platform_launches.windows` and `.linux` are explicit; no executable is inferred from an Egg or another platform. The selected executable and optional working directory are resolved beneath the managed server root, symlinks are evaluated, and the expected executable must be a regular file after installation. Declared ports are resolved from validated integer variables and inserted with the server and template provenance in one database transaction. Existing provisioned servers retain their concrete executable, arguments, ports, stop settings, and source/version snapshot when a catalog template changes.

Project Zomboid demonstrates a script-backed upstream distribution without script execution. The Windows Steam depot exposes `StartServer64.bat` plus `ProjectZomboid64.json`; the Official template translates the reviewed launcher data into the bundled `jre64/bin/java.exe` and a fixed argument slice. Its Windows Java classpath is one argv value containing relative entries separated by `;`. Validation permits that separator only immediately after `-cp`/`-classpath`, rejects absolute/traversal entries, and never parses it as command syntax. `-cachedir=.` keeps generated configuration, saves, logs, and first-boot state in the managed server root.

# Versioned game configuration adapters

Official files are grouped by game. `template.json` references adapter basenames in the same directory; `CatalogManager` resolves them relative to the catalog entry, fetches them through the existing fixed-origin client, validates them independently, and stores them beside the template in the last-good cache. A malformed or unavailable update retains a compatible last-good adapter. Without a valid current or cached adapter, the base template remains visible but provisioning is disabled so configuration values cannot be silently ignored.

Adapter definitions are data, while implementations are compiled Go code under `internal/gameconfig`. Schema v1 supports `xml-properties`, flat `ini-key-values`, and `section-tuple-key-values`. The bounded XML implementation updates unique declared property attributes. The INI implementation accepts only UTF-8, sectionless `key=value` records, splits on the first equals sign, and preserves BOM, comments, ordering, unknown keys, and line endings. The section/tuple implementation selects one descriptor-declared section and parenthesized container, decodes only mapped typed values, and preserves unknown or balanced opaque tuple properties. Duplicate mapped properties, malformed records, newline-bearing replacement values, oversized documents, unsafe targets, and invalid typed values fail closed.

During provisioning, ordinary adapters are applied after installation and before server publication. A reviewed `seed-from-file` initialization can atomically create a missing target from a parsed server-root-relative source without replacing an existing target. A `post_start_only` INI adapter is instead snapshotted without creating its target because some games, including Project Zomboid, generate authoritative defaults only on first start. The API/UI reports it as pending until the file exists. Exact validated JSON, adapter schema/version, and template ID/version are inserted into `server_config_adapters` in the same transaction as the normal server and ports. Runtime reads and edits always use this snapshot rather than the current remote catalog. Each successful edit writes `.gamenode-backups/<target>.previous`, atomically replaces the target, and reports restart-required state without changing process lifecycle.
