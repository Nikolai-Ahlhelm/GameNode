# API

All endpoints are namespaced below `/api/v1` and return errors as `{ "error": { "code": "...", "message": "..." } }`.

RBAC administration uses `GET /permissions` (the authoritative catalog with allowed scopes), role CRUD at `/roles`, subject assignments at `/users/{id}/roles` and `/groups/{id}/roles`, and `GET /servers/{id}/access` for server-scoped assignment listings. Role mutations require CSRF and `Roles.Manage`; catalog and assignment reads require `Roles.View`. Assignments now accept a third scope, `{"scope_type":"tenant","scope_id":"<tenant id>"}`, alongside the existing `global` and `server` scopes; see [RBAC management](#rbac-management).

The API integration suite covers setup/login/logout, server CRUD/lifecycle, WebSocket console authorization and malformed-frame handling, filesystem upload/download and sandboxing, RBAC scope enforcement, settings, diagnostics, audit, ports, and support-bundle authorization/ZIP behavior. The 2026-08-11 native Windows acceptance harness additionally exercised the embedded binary's setup, lifecycle, console, RBAC, and filesystem routes.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/setup/status` | Whether initial setup is required |
| POST | `/api/v1/setup` | Creates the first administrator while setup is open |
| POST | `/api/v1/auth/login` | Authenticates and creates a session cookie |
| POST | `/api/v1/auth/logout` | Revokes the current session |
| GET | `/api/v1/auth/me` | Current user and CSRF token |
| GET, POST | `/api/v1/users` | List with `Users.View`; create with `Users.Manage` |
| GET, PATCH, DELETE | `/api/v1/users/{id}` | Read with `Users.View`; update/delete with `Users.Manage` |
| GET | `/api/v1/users/{id}/groups` | List memberships; requires both `Users.View` and `Groups.View` |
| POST | `/api/v1/users/{id}/password` | Reset a local user's password with `Users.Manage` |
| GET, POST | `/api/v1/groups` | List with `Groups.View`; create with `Groups.Manage` |
| GET, PATCH, DELETE | `/api/v1/groups/{id}` | Read with `Groups.View`; update/delete with `Groups.Manage` |
| GET, POST | `/api/v1/groups/{id}/members` | List with `Groups.View`; add with `Groups.Manage` |
| DELETE | `/api/v1/groups/{id}/members/{userId}` | Remove a group member with `Groups.Manage` |
| GET | `/api/v1/permissions` | Lists the compiled RBAC permission catalog with `Roles.View` |
| GET, POST | `/api/v1/roles` | List with `Roles.View`; create with `Roles.Manage` |
| GET, PATCH, DELETE | `/api/v1/roles/{id}` | Read with `Roles.View`; update/delete with `Roles.Manage` |
| GET, PUT | `/api/v1/roles/{id}/permissions` | Read with `Roles.View`; replace with `Roles.Manage` |
| GET, POST | `/api/v1/users/{id}/roles` | List with `Roles.View`; assign with `Roles.Manage` |
| DELETE | `/api/v1/users/{id}/roles/{assignmentId}` | Remove a user assignment with `Roles.Manage` |
| GET, POST | `/api/v1/groups/{id}/roles` | List with `Roles.View`; assign with `Roles.Manage` |
| DELETE | `/api/v1/groups/{id}/roles/{assignmentId}` | Remove a group assignment with `Roles.Manage` |
| GET | `/api/v1/dashboard` | Basic authenticated dashboard information, filtered to visible servers only |
| GET, POST | `/api/v1/tenants` | List with `Tenants.View`; create with `Tenants.Manage` |
| GET, PATCH, DELETE | `/api/v1/tenants/{id}` | Read with `Tenants.View`; update/delete with `Tenants.Manage`; `id` is immutable, delete requires zero servers |
| GET, POST | `/api/v1/tenants/{id}/members` | List with `Tenants.View`; add with `Tenants.Manage`; membership alone grants no permission |
| DELETE | `/api/v1/tenants/{id}/members/{userId}` | Remove a tenant member with `Tenants.Manage` |
| GET | `/api/v1/tenants/{id}/servers` | Every server owned by this tenant, with `Tenants.View` |
| GET | `/api/v1/tenants/{id}/access` | RBAC assignments scoped to this tenant, with `Roles.View` (reuses the existing assignment tables/endpoints; assign with the ordinary `POST /users/{id}/roles` or `/groups/{id}/roles` using `"scope_type":"tenant"`) |
| GET | `/api/v1/servers` | Lists registered servers and their known runtime state |
| GET | `/api/v1/servers/creatable-tenants` | The tenants the caller may create a managed server in (any authenticated user; empty list if none). Exists so the Create Server/Game Library UI can offer or lock a tenant selector without requiring `Tenants.View` - it reveals only `id`/`name`, the same information already shown on every server the caller can see |
| POST | `/api/v1/servers` | Creates or adopts a native server definition; accepts an arbitrary `working_directory`, so it requires *global* `Server.Create` even though the permission itself also supports tenant scope (see Tenant isolation below) |
| GET | `/api/v1/servers/{id}` | Reads one server and runtime state; response includes `tenant_id`/`tenant_name` |
| PATCH | `/api/v1/servers/{id}` | Updates a stopped server definition; `tenant_id` in the body is ignored, a server's tenant is immutable after creation |
| DELETE | `/api/v1/servers/{id}` | Force-terminates an active process and deletes the server definition; files are retained |
| POST | `/api/v1/servers/{id}/start` | Starts the native application |
| POST | `/api/v1/servers/{id}/stop` | Stops it with timeout escalation |
| POST | `/api/v1/servers/{id}/restart` | Stops then starts it |
| POST | `/api/v1/servers/{id}/kill` | Immediately terminates it |
| GET, POST | `/api/v1/servers/{id}/ports` | List with `Ports.View`; add with `Ports.Manage` |
| PATCH, DELETE | `/api/v1/servers/{id}/ports/{portId}` | Update or remove with `Ports.Manage` |
| GET | `/api/v1/servers/{id}/monitoring` | Current health and process metrics; requires `Monitoring.View` for this server |
| GET | `/api/v1/servers/{id}/monitoring/history` | Bounded chronological process samples; requires `Monitoring.View` for this server |
| GET | `/api/v1/servers/{id}/files?path=...` | Lists one server-root-relative directory |
| DELETE | `/api/v1/servers/{id}/files?path=...&recursive=true` | Deletes a file or explicitly recursive directory |
| GET | `/api/v1/servers/{id}/files/content?path=...` | Reads one bounded UTF-8 text file |
| PUT | `/api/v1/servers/{id}/files/content` | Atomically replaces an existing text file |
| POST | `/api/v1/servers/{id}/files/file` | Creates a new text file without overwrite |
| POST | `/api/v1/servers/{id}/files/directory` | Creates one directory with an existing parent |
| POST | `/api/v1/servers/{id}/files/move` | Renames or moves a file or directory inside the root |
| POST | `/api/v1/servers/{id}/files/upload?path=...` | Streams one multipart file into a server-root-relative directory |
| GET | `/api/v1/servers/{id}/files/download?path=...` | Streams one regular file as an attachment |

## Files API

Files are scoped to the server's configured `working_directory`. The optional `path` query value is always relative to that root; the empty path (or `.`) lists the root. Absolute paths, drive and UNC paths, traversal segments, and targets outside the root are rejected.

Directory responses contain `entries` with `name`, root-relative slash-separated `path`, `type` (`directory` or `file`), `size`, and `modified_at`. Directories sort before files, then alphabetically. Listings are non-recursive and capped at 10,000 entries.

Content responses contain `path`, `size`, `modified_at`, `encoding` (`utf-8`), and `content`. Only regular UTF-8 text files up to 4 MiB are readable. Binary, special, and larger files are rejected from this JSON endpoint.

Files are RBAC-enforced per server: `Files.View` lists and reads text content; `Files.Download` authorizes downloads; `Files.Edit` creates files/directories and writes text; `Files.Rename` moves or renames; `Files.Delete` removes content; and `Files.Upload` authorizes uploads. These permissions are independent: for example, `Files.View` does not imply download or edit access.

All file mutations require the normal administrator session, same-origin validation, and `X-CSRF-Token`. Mutation JSON uses only relative paths: `{"path":"config/server.properties","content":"..."}` for creates/writes, and `{"source":"old.txt","destination":"archive/old.txt"}` for moves. Create operations return a conflict when the target exists; directory creation is non-recursive. Writes replace existing regular text files through a temporary file in the same directory followed by an atomic replacement. Deletes are non-recursive unless `recursive=true` is explicitly supplied; the server root itself cannot be deleted.

### Upload and download

`POST /files/upload` accepts `multipart/form-data` with exactly one first part named `file`. The `path` query parameter is the existing, server-root-relative target directory; it defaults to the root. The multipart parser canonicalizes a submitted filename to its basename before it reaches the API, and the resulting value is then validated as a filename only. Any remaining separators, traversal, drive/UNC syntax, control characters, and platform-unsafe characters are rejected. The submitted filename is never treated as a path. Existing files are rejected by default. `overwrite=true` is required to atomically replace an existing regular file.

Uploads stream into a temporary file in the validated target directory. The temporary file is synced and committed atomically only after the whole transfer succeeds; aborted, invalid, and oversized uploads leave no final target or temporary upload file. The global `filesystem.max_upload_bytes` setting defaults to 64 MiB.

`GET /files/download` streams a regular file with `application/octet-stream`, `Content-Length`, `X-Content-Type-Options: nosniff`, and a safely encoded attachment filename. It supports binary and large files without loading them into application memory. HTTP range requests are not implemented in 4C.

## Console WebSocket

`GET /api/v1/servers/{id}/console/ws` upgrades an authenticated administrator session to the console transport. The server first sends `{ "type": "console", "state": "attached" }`, `detached`, or `closed`. Attached sessions then replay the bounded in-memory history followed by live `output`/`state` events. Output keeps its `stdout` or `stderr` stream and timestamp.

Clients may send `{ "type": "input", "data": "status\\n" }`. Input is limited to the console input limit and is only accepted for an attached session. Browser reconnects may replay history; the v0.1 UI resets its local view on a successful reconnect.

Malformed JSON or frames beyond the configured WebSocket read limit terminate that connection without forwarding input. A closed or stopped server reports `closed`; detached servers report `detached` and never receive a synthetic session.

All server mutations require an authenticated administrator, same-origin validation, and `X-CSRF-Token`. Server create/update payloads use `arguments` as a JSON string array and `environment_variables` as a JSON object; neither is parsed as a shell command.

## Local users and groups

User reads require `Users.View` and user mutations require `Users.Manage`; group reads require `Groups.View` and group mutations require `Groups.Manage`. Role/catalog and assignment reads require `Roles.View`, while their mutations require `Roles.Manage`. Platform permissions are effective only through global assignments; server-scoped platform assignments do not grant management access. All mutating requests require the normal same-origin and CSRF checks. `GET /auth/me` remains available to every authenticated user and returns only effective global capabilities.

Usernames are 3–32 ASCII characters; group names are 2–64 ASCII characters. Both permit letters, digits, `.`, `_`, and `-`, and are unique case-insensitively. Unicode identifiers are rejected rather than relying on incomplete Unicode case folding or normalization. Groups do not imply administrator access, but group role assignments contribute to the member’s effective RBAC permissions.

## RBAC management

Roles contain permissions selected from the static `/permissions` catalog. A role can be assigned to a user or group at global scope (`{"scope_type":"global"}`), at one existing tenant (`{"scope_type":"tenant","scope_id":"<tenant id>"}`), or at one existing server (`{"scope_type":"server","scope_id":"<server id>"}`). `Roles.View` and `Roles.Manage` control the corresponding read and mutation routes; mutating calls require the normal same-origin and CSRF protections. Role names have no special meaning, and `Roles.Manage` cannot add keys outside the compiled catalog. Roles stay scope-neutral; only an assignment carries a scope.

The permission catalog is authoritative for `allowed_scopes`. `Server.View`, the remaining per-server lifecycle permissions except `Server.Create`, Console, Files, Ports, and `Monitoring.View` allow `global`, `tenant`, and `server`: a global or tenant assignment applies to every server it reaches, while a server assignment applies only to its named server. `Server.Create` allows only `global` and `tenant` - never `server`, since a server does not exist yet at the moment `Server.Create` is evaluated; a global grant may create a server in any tenant, a tenant grant only inside that one tenant. Users, Groups, Roles, Settings, Logs, Templates, Audit, and the new `Tenants.View`/`Tenants.Manage` permissions (administering tenant entities themselves, not access to resources inside one) are global-only.

`server_assignable`/`tenant_assignable` are true only when a role has at least one permission and every permission allows that scope; both are false for an empty role, a global-only role, or a role mixing a global-only permission with others. Creating an assignment with an unsuitable role at that scope is rejected with a controlled validation error, and a role that already has server- or tenant-scoped assignments must remain suitable for that scope: it cannot be emptied or changed to include an unsuitable permission until those assignments are removed first. Global assignments accept every current catalog permission because every permission allows global scope.

Tenant membership (`internal/tenants`) is not authorization: belonging to a tenant grants no permission by itself. Only a role assignment - direct or through group membership, at global, tenant, or server scope - makes a permission effective.

## Product authorization

Authenticated enabled administrators retain a full bypass; a disabled user is denied before that bypass is ever considered. Other users are evaluated through their direct and group role assignments. For a permission evaluated against a specific server, it is effective when any of the following holds: a direct or group **global** assignment; a direct or group **tenant** assignment for that server's own tenant; or a direct or group **server** assignment for that exact server. A permission evaluated at tenant scope (with no server involved, e.g. `Server.Create`) considers only the global and tenant branches. Permissions are allow-only, have no deny rules, and have no implicit inheritance - `Manage` never implies `View`, and a role's suitability for a scope is never applied partially.

`Server.View` controls list/detail visibility; `Server.Create` is global- or tenant-scoped (never server-scoped, see above); `Server.Edit`, `Server.Delete`, `Server.Start`, `Server.Stop`, `Server.Restart`, and `Server.Kill` control their matching actions. The server list filters entries without `Server.View`. `Monitoring.View` is tenant/server-scoped the same way: global or tenant assignments apply to every reachable server and a server assignment applies only to that server.

## Monitoring

The monitoring endpoint returns current runtime/health state, verified PID, uptime, CPU percentage, resident memory, optional thread/handle counts, exit data, and persisted crash/restart counters. History is in-memory, chronological, and bounded to 300 samples per server by default; the default sampling interval is five seconds.

Server create/update payloads accept `auto_restart_enabled`, `auto_restart_max_attempts`, `auto_restart_window_seconds`, and `auto_restart_delay_seconds`. This `Server.Edit` configuration schedules only unexpected non-zero exits; monitoring reports enabled/pending policy state and attempts in the rolling window.

## Application settings

`GET /api/v1/settings` returns the whitelisted application settings and requires the global-only `Settings.View` permission. `PATCH /api/v1/settings` requires global-only `Settings.Manage`, same-origin validation, and `X-CSRF-Token`; the permissions are independent. Server-scoped assignments do not grant either permission.

The typed surface includes `monitoring.sample_interval_seconds` (1–300), `monitoring.history_limit` (1–10,000), `logging.level`, `branding.name` (1–64 characters), `branding.subtitle` (0–128 characters), and the live password policy fields `security.password_minimum_length` (8–128, default 8) and `security.password_maximum_length` (at least the minimum and at most 256, default 256). Branding and password-policy changes apply immediately; existing passwords are not invalidated. PATCH accepts only whitelisted typed fields and rejects unknown fields. IPv4/IPv6, database, TLS, session, filesystem, executable, environment, and arbitrary YAML values are not settings API fields. A successful mutation records one `settings.update` audit event containing only changed field names.

`PUT /api/v1/settings/favicon` and `DELETE /api/v1/settings/favicon` require global `Settings.Manage` and CSRF. Uploads are bounded to 256 KiB and accept validated PNG images up to 512×512 or structurally bounded ICO files; SVG and remote URLs are not accepted. `GET /api/v1/branding/favicon` is public so browsers can load the current icon, returns only the validated stored bytes with `nosniff`, and returns 404 when no custom favicon exists.

## Diagnostics

`GET /api/v1/diagnostics` is read-only and requires global-only `Settings.View`. It reports safe application/runtime, platform, SQLite schema-health, and monitoring configuration summaries. Release binaries additionally report the injected semantic version, commit SHA, and UTC build time in `application`; development builds report `dev`. It never returns paths, environment values, credentials, network adapters, server roots, or database contents, and it executes no shell commands.

## Support bundle

`POST /api/v1/support/bundle` requires authentication, global-only `Settings.Manage` (server-scoped assignments do not apply), same-origin validation, and `X-CSRF-Token`. It returns `200 application/zip` with `Content-Disposition: attachment` and a server-generated safe `gamenode-support-<UTC>.zip` filename. The fixed archive contains only `manifest.json`, `diagnostics.json`, `settings.json`, `audit-recent.json`, and `servers.json`; core output is capped at 10 MiB. Generation completes in a bounded in-memory buffer before ZIP headers are committed, so a generation failure returns a controlled JSON API error rather than a partial download. There are no other support endpoints.

## Server ports

Port records contain `id`, `name`, `protocol`, `bind_address`, `port`, and dynamic `status`. Protocol is `tcp` or `udp`; ports are 1–65535. Bind addresses may be empty/wildcard, `0.0.0.0`, `::`, or concrete IPv4/IPv6 literals. Hostnames are unsupported. `Ports.View` and `Ports.Manage` are independent server-scoped permissions: global grants apply everywhere and server grants only at that server. Mutations require the normal CSRF token. Validation can reject invalid ports, protocols or addresses, an internal registry collision, or an externally occupied OS port; unknown servers/ports and missing permissions use the normal API errors.

Console WebSocket connections require `Console.View`. `Console.Send` is checked separately for every inbound `input` message: view-only clients can receive state, history, and output but receive `{"type":"error","state":"permission_denied"}` for input. Live output also rechecks `Console.View`, so removing access or disabling a user stops an active subscriber at the next event.

An enabled administrator bypasses these checks. `Users.Manage` does not permit setting or clearing `is_admin`; only an active administrator can change that flag, and last-active-admin protection remains independent. `Roles.Manage` may delegate catalogized RBAC permissions, but cannot create unknown permission keys or an administrator bypass.
# Audit log

`GET /api/v1/dashboard` is read-only and returns capability-filtered server, monitoring, port, and (only with global `Audit.View`) recent audit summaries. It never reports hidden servers or performs port scans or mutations.

`GET /api/v1/audit` is a read-only, global audit endpoint. It requires the global-only `Audit.View` permission (a server-scoped assignment does not grant access); administrators retain the normal bypass. It accepts bounded `limit` (default 100, maximum 500) and `offset`, plus `actor_user_id`, `action`, `resource_type`, `resource_id`, `server_id`, and `result` filters. The optional bounded `query` filter searches action, actor snapshot, resource type/name/IDs, server ID, and controlled error fields; SQL wildcard characters in user input are treated literally. Results are newest first (`timestamp DESC`, `id DESC`) and return `items`, `limit`, and `offset`. Each item uses lower-snake-case JSON fields such as `timestamp`, `actor_username`, and `resource_id`, and contains its persisted actor/resource snapshots, result, direct remote IP, controlled metadata, and sanitized error fields. Deleted resources remain visible through those snapshots. GameNode exposes no audit mutation, clear, or delete endpoint.
# Templates API

Templates are global node resources. `Templates.View` and `Templates.Manage` are independent and global-only.

| Method | Path | Authorization and behavior |
|---|---|---|
| `POST` | `/api/v1/templates/analyze/egg` | `Templates.Manage` + CSRF; normalize and return a preview without persistence |
| `POST` | `/api/v1/templates/import/egg` | `Templates.Manage` + CSRF; normalize, persist, and audit exactly one import mutation |
| `GET` | `/api/v1/templates` | `Templates.View`; list normalized templates |
| `GET` | `/api/v1/template-catalog` | `Templates.View`; return validated Official templates plus remote/cache/offline status; never triggers network I/O |
| `POST` | `/api/v1/template-catalog/refresh` | `Templates.View` + CSRF; refresh the fixed Official source, returning cached data on remote failure when available |
| `GET` | `/api/v1/templates/{id}` | `Templates.View`; read one normalized template |
| `DELETE` | `/api/v1/templates/{id}` | `Templates.Manage` + CSRF; delete and audit an imported template; Official/built-in templates return `409` |
| `GET` | `/api/v1/templates/{id}/provisionability` | `Templates.View` (global) + global `Server.Create`; whether this node can install this template is tenant-independent, so this check stays global-only by design |
| `POST` | `/api/v1/templates/{id}/provision` | Global `Templates.View` + CSRF + `Server.Create` effective for the request's `tenant_id` (global or that tenant); start an asynchronous native provision |
| `POST` | `/api/v1/templates/{id}/resolve` | `Templates.View` + `Server.Create`; inspect an existing directory and preview a built-in resolver result |
| `POST` | `/api/v1/templates/{id}/adopt` | `Templates.View` (global) + **global** `Server.Create` + CSRF; create a normal server from a resolved existing installation at an arbitrary admin-supplied path, so - like `POST /servers` - it deliberately never accepts tenant-scoped `Server.Create` |

`POST /templates/{id}/provision`'s body is `{"tenant_id":"...","server_name":"...","directory_name":"...","variables":{...}}`. `tenant_id` selects which tenant the new managed server belongs to; left empty it defaults to the default tenant. Authorization requires global `Templates.View` (can this node install this kind of template at all - tenant-independent) **and** `Server.Create` effective for that tenant (a global grant may provision into any tenant, a tenant-scoped grant only into its own tenant; tenant membership alone never satisfies this). An unknown `tenant_id` fails with a controlled `400 invalid_tenant` for an authorized (global) caller, or `403` for a tenant-scoped caller whose grant cannot possibly match an ID that names no tenant - neither ever falls through to an internal error. The directory is always resolved under `<data>/tenants/<tenant-id>/servers/<directory>`; the body has no field for a host path, so a tenant-scoped grant can never point a managed install anywhere else. Custom Application and Adopt Existing (`POST /servers`, `POST /templates/{id}/adopt`) remain global-`Server.Create`-only for exactly this reason: both accept an arbitrary admin-supplied `working_directory`/`server_root`, and a tenant-scoped `Server.Create` grant must never unlock that.

Analyze/import accept `{"egg": <Egg JSON object>}`. Upload and pasted JSON clients use the same bounded representation. URL input is not supported. Bodies over the bounded envelope or Eggs over 256 KiB return `413 egg_too_large`; invalid Eggs return a controlled `422 invalid_egg` without raw parser errors. Responses use the GameNode template model and never return the original Egg or installation script. Sensitive defaults are discarded before a response or database write.

The NeoForge resolve/adopt body is `{"server_name":"...","server_root":"absolute existing path","minimum_memory_mb":1024,"maximum_memory_mb":4096,"nogui":true}`. Resolve returns detected NeoForge/Minecraft versions, platform argfile launch, Java discovery state, working directory, and stop semantics. Adopt fails with `java_not_found` unless Java is available through `JAVA_HOME` or `PATH`; it does not alter the selected installation.

`GET /template-catalog` reports `source` (`remote`, `cache`, or `none`), `fetched_at`, `cached`, `offline`, a bounded generic `last_error`, and the count of isolated invalid templates. Refresh returns `503 official_catalog_unavailable` only when no valid Official data exists; a last-good cache is returned with `200` and offline status. Catalog reads and manual refreshes are not audited.

# Provisioning API

Provisioning is intentionally template-specific rather than a generic job execution API.

| Method | Path | Authorization and behavior |
| --- | --- | --- |
| `GET` | `/api/v1/provisioning/jobs/{id}` | Initiating user or admin; return safe persisted status and bounded chronological events |
| `POST` | `/api/v1/provisioning/jobs/{id}/cancel` | Initiating user or admin + CSRF; cancel an active installer |
| `POST` | `/api/v1/provisioning/jobs/{id}/retry-registration` | Initiating user or admin + CSRF; retry only a recoverable persisted registration snapshot without running SteamCMD again |

The start body is bounded to 128 KiB and has the form `{"server_name":"...","directory_name":"...","variables":{"KEY":"value"}}`. `directory_name` is a relative storage name, not a path; the target is always resolved below `<data>/servers`. Unknown/non-editable variables, invalid normalized values, unsupported platform plans, populated targets, and unsafe SteamCMD options are rejected with controlled errors.

For Official SteamCMD templates, `GET /templates/{id}/provisionability` additionally reports the fixed installer, App ID, validation flag, selected host platform, and selected launch executable for review. `POST /templates/{id}/provision` never accepts an App ID, login, command, installer URL, or argument array. It requires both `Templates.View` and `Server.Create` plus CSRF, just like imported template provisioning.

| Method | Path | Authorization and behavior |
|---|---|---|
| `GET` | `/api/v1/servers/{id}/configuration` | `Server.View` in server scope; reads typed fields through the server's persisted adapter snapshot |
| `PUT` | `/api/v1/servers/{id}/configuration` | `Server.Edit` in server scope + CSRF; validates and atomically updates one adapter |

The update body is `{"adapter_id":"...","values":{"FIELD":"value"}}`. Unknown adapters/fields, invalid typed values, unsafe XML/INI, missing or duplicate properties, and unsafe targets are rejected. Each adapter reports `ready` and an optional `status_message`; a post-start adapter returns its typed field shape with `ready:false` until the game creates the target, and PUT returns the normal unavailable/not-found response. Secret values are accepted only in the mutation body, are never returned, and are omitted from audit metadata. An empty secret omitted from `values` leaves the current value unchanged. Responses report `restart_required`; they never restart the server automatically.

Both endpoints are semantic: a field describes the meaning of a game setting, not its storage. The response `format` distinguishes a file-backed adapter (`xml-properties`, `ini-key-values`, `section-tuple-key-values`, with a `target` path) from a schema-v2 `managed-launch` adapter, whose values GameNode stores itself and applies to the native launch at start. A `managed-launch` adapter reports an empty `target` and is always `ready`, because it does not wait for the game to generate a file. Clients render both from the same field metadata.

Start returns `202` with a job. Statuses are `pending`, `preparing`, `downloading_steamcmd`, `steamcmd_ready`, `installing`, `steamcmd_completed`, `validating_installation`, `installation_validated`, `resolving_launch`, `registering_server`, `server_registered`, `completed`, `failed`, or `cancelled` (`creating_server` remains accepted for persisted compatibility). Responses contain phase summaries, failure classification, `installation_completed`, `registration_recoverable`, `files_may_remain`, and at most 200 safe chronological events, but never the registration snapshot, target absolute paths, raw SteamCMD output after restart, variable values, credentials, or command lines. A completed job contains the normal `server_id`. Registration retry is serialized and idempotent: it reuses the persisted normalized snapshot, recognizes an already committed server, and never invokes SteamCMD. A job whose managed configuration contains a secret value stores no registration snapshot and reports `registration_recoverable: false`, because replaying it would create a server with silently missing secrets; its failure summary tells the operator to provision again.

Supported source fields include `meta.version`, `exported_at`, `name`, `description`, `author`, `uuid`, `startup`, `variables`, `docker_images` (metadata only), `scripts.installation` (analysis only), `config`, `features`, and tags. Unknown top-level fields become informational findings. Config parser bodies, file rewrite rules, Docker semantics, arbitrary installation hooks, and unknown config structures are not executed and may produce compatibility findings.

Supported variable rules are `required`, `nullable`, `integer`, `numeric`, `string`, `boolean`, `between`, `min`, `max`, and `in`. Other Laravel/Pterodactyl rules remain in `raw_rules` and produce `UNKNOWN_VALIDATION_RULE`; GameNode does not emulate the full validation language.
