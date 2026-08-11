# API

All endpoints are namespaced below `/api/v1` and return errors as `{ "error": { "code": "...", "message": "..." } }`.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/setup/status` | Whether initial setup is required |
| POST | `/api/v1/setup` | Creates the first administrator while setup is open |
| POST | `/api/v1/auth/login` | Authenticates and creates a session cookie |
| POST | `/api/v1/auth/logout` | Revokes the current session |
| GET | `/api/v1/auth/me` | Current user and CSRF token |
| GET, POST | `/api/v1/users` | List with `Users.View`; create with `Users.Manage` |
| GET, PATCH, DELETE | `/api/v1/users/{id}` | Read, update, or delete a local user (administrator only) |
| POST | `/api/v1/users/{id}/password` | Reset a local user's password (administrator only) |
| GET, POST | `/api/v1/groups` | List or create local groups (administrator only) |
| GET, PATCH, DELETE | `/api/v1/groups/{id}` | Read, update, or delete a local group (administrator only) |
| GET, POST | `/api/v1/groups/{id}/members` | List or add group members (administrator only) |
| DELETE | `/api/v1/groups/{id}/members/{userId}` | Remove a group member (administrator only) |
| GET | `/api/v1/permissions` | Lists the compiled RBAC permission catalog (administrator only) |
| GET, POST | `/api/v1/roles` | List or create roles (administrator only) |
| GET, PATCH, DELETE | `/api/v1/roles/{id}` | Read, update, or delete a role (administrator only) |
| GET, PUT | `/api/v1/roles/{id}/permissions` | Read or replace a role's permissions (administrator only) |
| GET, POST | `/api/v1/users/{id}/roles` | List or assign a role to a user (administrator only) |
| DELETE | `/api/v1/users/{id}/roles/{assignmentId}` | Remove a user role assignment (administrator only) |
| GET, POST | `/api/v1/groups/{id}/roles` | List or assign a role to a group (administrator only) |
| DELETE | `/api/v1/groups/{id}/roles/{assignmentId}` | Remove a group role assignment (administrator only) |
| GET | `/api/v1/dashboard` | Basic authenticated dashboard information |
| GET | `/api/v1/servers` | Lists registered servers and their known runtime state |
| POST | `/api/v1/servers` | Creates or adopts a native server definition |
| GET | `/api/v1/servers/{id}` | Reads one server and runtime state |
| PATCH | `/api/v1/servers/{id}` | Updates a stopped server definition |
| DELETE | `/api/v1/servers/{id}` | Deletes a stopped server definition |
| POST | `/api/v1/servers/{id}/start` | Starts the native application |
| POST | `/api/v1/servers/{id}/stop` | Stops it with timeout escalation |
| POST | `/api/v1/servers/{id}/restart` | Stops then starts it |
| POST | `/api/v1/servers/{id}/kill` | Immediately terminates it |
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

Roles contain permissions selected from the static `/permissions` catalog. A role can be assigned to a user or group at global scope (`{"scope_type":"global"}`) or at one existing server (`{"scope_type":"server","scope_id":"..."}`). All RBAC-management endpoints are administrator-only and mutating calls require the normal same-origin and CSRF protections.

## Product authorization

Authenticated enabled administrators retain a full bypass. Other users are evaluated through their direct and group role assignments; global assignments apply everywhere and a server-scoped assignment applies only to that server. Permissions are allow-only and have no implicit inheritance.

`Server.View` controls list/detail visibility; `Server.Create` is global-only; `Server.Edit`, `Server.Delete`, `Server.Start`, `Server.Stop`, `Server.Restart`, and `Server.Kill` control their matching actions. The server list filters entries without `Server.View`.

Console WebSocket connections require `Console.View`. `Console.Send` is checked separately for every inbound `input` message: view-only clients can receive state, history, and output but receive `{"type":"error","state":"permission_denied"}` for input. Live output also rechecks `Console.View`, so removing access or disabling a user stops an active subscriber at the next event.

An enabled administrator bypasses these checks. `Users.Manage` does not permit setting or clearing `is_admin`; only an active administrator can change that flag, and last-active-admin protection remains independent. `Roles.Manage` may delegate catalogized RBAC permissions, but cannot create unknown permission keys or an administrator bypass. `Settings.View`, `Settings.Manage`, `Monitoring.View`, and `Audit.View` remain reserved until matching product endpoints exist.
