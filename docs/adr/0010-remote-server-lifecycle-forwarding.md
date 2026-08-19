# ADR 0010: Remote Server Lifecycle Forwarding (v0.5B)

## Status

Accepted (backend, controller API, and controller UI implemented and tested;
see PROJECT_PLAN.md).

## Context

v0.5A (docs/adr/0007-remote-node-foundation.md) established a
machine-authenticated Node API for identity/health/capability exchange
between GameNode installations, and a controller-side registry
(`internal/nodes`) that tracks enrolled Remote Nodes. It deliberately
stopped short of doing anything with a remote node's own servers.

v0.5B needs the controller to list, create, edit, delete, and control the
lifecycle (start/stop/restart/kill) of servers that live on an enrolled
Remote Node - without ever becoming a second lifecycle implementation, and
without the controller touching that node's database, process table, or
container runtime directly.

## Decision

1. **The remote node remains the sole lifecycle authority.** Every new
   Node API endpoint under `/api/v1/node/servers...`
   (`internal/api/node_servers.go`) is a thin, machine-authenticated
   forwarder onto that SAME node's own `internal/servers.Service` - the
   identical code path a local browser session would drive through
   `internal/api/servers.go`. There is no parallel implementation of
   create/start/stop/etc.
2. **The controller never simulates or caches an authoritative state.**
   `internal/remote.Client` gets new typed, bounded methods
   (`ListServers`, `GetServer`, `CreateServer`, `UpdateServer`,
   `DeleteServer`, `StartServer`/`StopServer`/`RestartServer`/`KillServer`)
   that call exactly those Node API paths. The controller-side handlers in
   `internal/api/remoteservers.go` return exactly what the node reported;
   "node unreachable" is never presented as "server stopped" (continuing
   v0.5A's rule).
3. **A new capability, not a protocol version bump.** `nodeidentity`
   gains `CapabilityRemoteServerManagement` (`remote_server_management`).
   Existing v0.5A endpoints (info/health/capabilities/enroll) are
   unchanged - `nodeidentity.ProtocolVersion` stays at 1. A controller
   talking to an older node without this capability gets a controlled
   `501 node_capability_unsupported` from `requireEnabledRemoteNode`
   instead of attempting the call.
4. **Bounded, typed responses only, never a local path.** The Node API's
   `nodeServer` projection deliberately excludes `WorkingDirectory`,
   `Executable`, `Arguments`, `EnvironmentVariables`, `StopCommand`, and
   `Container` - none of that needs to cross the machine boundary for
   remote list/get/lifecycle. `CreateServerInput` is the one place a
   working directory travels, and only as INPUT the operator explicitly
   typed for that specific target node.
5. **Create is deliberately global-permission-only**, mirroring the
   existing local `Server.Create` handler-level restriction
   (`internal/api/servers.go`): a create request can point at an arbitrary
   path on the target node, so a tenant-scoped `RemoteServer.Manage` grant
   must never reach it.
6. **Tenant isolation is re-checked against the node's own answer, every
   time.** `authorizeRemoteServer` (`internal/api/remoteservers.go`) always
   calls the node's `GetServer` before authorizing a per-server action and
   checks the permission against THAT authoritative `tenant_id` - never a
   client-supplied or locally cached one. A node cannot claim a server
   belongs to a tenant it doesn't.
7. **New RBAC permissions, not scope reuse.** `RemoteServer.View` /
   `RemoteServer.Manage` are distinct from local `Server.View`/`Server.*`.
   They support `global` and `tenant` scope only (see
   `internal/rbac/catalog.go`'s `isRemoteServerPermission`) - there is no
   local per-remote-server assignment row to scope a `server` grant
   against, since the server itself lives in the remote node's database.
8. **A distinct audit trail.** Every remote mutation/lifecycle action is
   recorded under `audit.RemoteServer` with dedicated actions
   (`remote_server.create`, `.update`, `.delete`, `.start`, `.stop`,
   `.restart`, `.kill`), tagged with the node id in metadata - never
   conflated with local `server.*` actions, and never recorded for a
   routine list/get read.

## Consequences

- A controller can manage a remote node's servers with the same security
  posture (RBAC, CSRF, tenant isolation, sanitized errors) as its own
  local servers, without any new trust boundary crossing.
- An old, unpatched remote node degrades safely: the controller reports
  `node_capability_unsupported` rather than guessing at an unsupported
  contract.
- The controller-facing UI is integrated into the Nodes detail surface. It
  exposes only the bounded fields and operations supported by this contract.
