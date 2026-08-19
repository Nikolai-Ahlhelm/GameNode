# ADR 0011: Remote Operational Hardening - Console, Files, Monitoring (v0.5C)

## Status

Accepted (backend, controller API, and controller UI implemented and tested;
see PROJECT_PLAN.md).

## Context

With v0.5B's server lifecycle forwarding in place, day-to-day remote
operation still needs console access, file editing, and monitoring/health
visibility for a server that lives on an enrolled Remote Node - the same
things an operator already has for a local server via
`internal/console`, `internal/filesystem`, and `internal/monitoring`.

The local console uses an interactive WebSocket (`internal/api/console.go`).
Remote operation needs the same experience without moving lifecycle/session
authority away from the target node.

## Decision

1. **Bounded WebSocket relay with polling fallback.** The node exposes the
   same bounded JSON console protocol at `/console/ws` behind machine auth.
   The controller opens one fixed second hop, checks browser View permission
   before upgrade and re-checks View/Send while forwarding messages. Older
   nodes or relay failures fall back to the existing bounded `Snapshot()` and
   `Input()` endpoints. The node's `console.Session` remains the sole owner.
2. **Console input keeps its own permission.** `RemoteConsole.View` and
   `RemoteConsole.Send` are separate permissions (mirroring local
   `Console.View`/`Console.Send`) - a read-only grant can never send input.
3. **Remote Files reuse the exact same sandbox, no "remote" special
   case.** The Node API's files endpoints call the SAME
   `internal/filesystem.Service` methods
   (`ListDirectory`/`ReadFile`/`WriteFile`/`CreateFile`/
   `CreateDirectory`/`Move`/`Delete`) rooted at the target server's
   `WorkingDirectory` on that node - identical sandboxing, traversal
   protection, and size limits as local file access. The controller
   forwards a path string; it never interprets or rewrites it, and never
   touches a filesystem itself.
4. **Binary upload/download uses typed fixed routes.** Multipart upload and
   binary download are forwarded only through named `internal/remote.Client`
   methods, use the same filesystem sandbox, and are capped at 64 MiB. There
   is no generic byte proxy or arbitrary remote URL.
5. **RemoteFiles permissions mirror local Files.\*** (`.View`, `.Edit`,
   `.Upload`, `.Download`,
   `.Delete`, `.Rename`) so a read-only grant can browse and read but never
   mutate - enforced entirely server-side; the UI hides unsupported
   affordances, but the backend remains the authority regardless of what any
   client renders.
6. **Monitoring is a thin, unaudited passthrough.** `GET
   /api/v1/node/servers/{id}/monitoring` returns the node's own
   `MonitoringSnapshot` - never audited, exactly like local
   `Monitoring.View` reads (see AGENTS.md's "no audit entries for routine
   polling" rule).
7. **Content is never audited.** Console `POST` and every file mutation
   record only metadata (byte counts, paths, the fact of the action) under
   `audit.RemoteConsole`/`audit.RemoteFile` actions - never the actual
   console line or file content, matching the existing local
   `Console.Send`/`Files.*` audit contract exactly.
8. **A new capability per surface**, not a protocol bump:
   `remote_console`, `remote_files`, `remote_monitoring` - each checked
   independently before the controller attempts a call, so a node that
   only advertises `remote_server_management` degrades the other three
   surfaces to a controlled `501 node_capability_unsupported` rather than
   a broken request.

## Consequences

- Remote console/files/monitoring get the same bounded, sanitized,
  RBAC/CSRF/tenant-isolated posture as v0.5B's server management. Relay
  connections are fixed, permission-checked, and closed on either hop.
- The controller-facing UI is integrated into the Nodes detail surface. It
  uses live relay with bounded polling fallback and binary file operations.
