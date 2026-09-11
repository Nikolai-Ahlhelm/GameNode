# ADR 0012: Remote Game Configuration

## Status

Accepted (backend, controller API, and controller UI implemented and tested).

## Context

ADR 0010 (remote-server-lifecycle-forwarding) and ADR 0011
(remote-operational-hardening) already give a controller bounded, typed,
RBAC/CSRF/tenant-isolated access to a remote server's lifecycle, console,
files, and monitoring - the same day-to-day surfaces a local server already
has through `internal/servers`, `internal/console`, `internal/filesystem`,
and `internal/monitoring`.

Local servers additionally have a structured, versioned, declarative
per-game configuration editor (`internal/gameconfig`): typed fields with
validated ranges/enums, sensitive-value redaction, and either safe
file-format editing or `managed-launch` binding - exposed at
`GET/PUT /api/v1/servers/{id}/configuration` and rendered by
`web/src/game-configuration.tsx`. This had no remote equivalent: a remote
server's configuration could only be edited as raw file text through the
Remote Files surface, which works but loses the typed fields, validation,
and secret-redaction the local editor already provides.

Separately, `POST /api/v1/cluster/placement/execute`
(docs/adr/0009-cluster-scheduling-decision-vs-execution.md,
docs/adr/0010-container-placement-execution.md) and
`POST /api/v1/remote-nodes/{id}/provisioning` already fully implement
deploying a Game Library template to a local or remote node - this was
backend-complete and tested, but had no usable frontend: the Nodes page's
"Execute native placement" form sent a payload the execute endpoint's
`DisallowUnknownFields` decoder rejects, and the container-runtime path had
no UI at all.

## Decision

1. **Remote Game Configuration follows the ADR 0011 template exactly, no
   exceptions.** A new `remote_gameconfig` capability
   (`internal/nodeidentity`), a new `RemoteConfig.View`/`RemoteConfig.Edit`
   permission pair (`global`/`tenant` scope, same as every other Remote*
   permission - a remote server has no local per-server assignment row to
   scope against), a new Node-facing route
   (`/api/v1/node/servers/{id}/configuration`) that calls
   `internal/gameconfig.Service` unchanged, and a new controller-facing
   proxy route (`/api/v1/remote-nodes/{id}/servers/{id}/configuration`) that
   authorizes against the server's authoritative tenant (re-fetched from the
   node, never cached) and forwards through a new pair of
   `internal/remote.Client` methods (`GetConfiguration`/
   `UpdateConfiguration`) using mirrored, bounded wire types
   (`RemoteConfiguration`/`RemoteConfigAdapter`/`RemoteConfigField`) rather
   than importing `internal/gameconfig` types directly into
   `internal/remote` - the same decoupling `ServerSummary`/`ConsoleEvent`/
   `MonitoringSnapshot` already use.
2. **No new domain logic anywhere.** `internal/gameconfig.Get`/`Update`
   already validate, sandbox, and redact sensitive values; this ADR adds a
   relay, not a second implementation.
3. **Content is never audited beyond bounded metadata**, matching the local
   `server.config_update` contract exactly: a successful or failed `PUT` is
   audited once as `remote_config.update` under the existing `RemoteServer`
   audit resource type, carrying only `node_id`, `adapter_id`, and
   `field_count` - never a field value. `GET` is never audited (read-only,
   matching Remote Monitoring's precedent).
4. **The Node-facing handler never audits locally.** The machine credential
   that authenticates a Node-facing call carries no human actor; only the
   controller side (which does have an authenticated user) records the
   audit event, tagged with the node id - matching every other node-facing
   lifecycle/files handler.
5. **The existing local `GameConfiguration` React component is reused
   unchanged apart from one new optional `basePath` prop**, rather than
   forked into a remote-specific copy. The Nodes UI renders it as a
   `Configuration` tab on a remote server's detail view, gated on the new
   capability and permission, pointed at the remote route.
6. **The already-complete template-deployment backend gets its missing
   frontend**, not new backend surface: `RemoteTemplateDeploy`
   (`web/src/remote-provisioning.tsx`) is a single form (no local-host-only
   provisionability precheck, since that precheck cannot speak for a
   different node's OS/arch/image policy) that either deploys to one
   explicitly chosen enrolled node
   (`POST /api/v1/remote-nodes/{id}/provisioning`) or lets cluster placement
   choose (`POST /api/v1/cluster/placement/execute`), polling whichever job
   endpoint the response indicates. The target node's own typed provisioning
   errors (`container_image_not_declared`, `not_provisionable`, ...) are
   surfaced verbatim instead of a fabricated local compatibility check.

## Consequences

- Remote configuration editing gets the same bounded, sanitized,
  RBAC/CSRF/tenant-isolated posture as every other v0.5B/v0.5C remote
  surface, with zero new persistence (a controller never stores a remote
  server's configuration values; the target node keeps sole ownership of
  its own `server_config_adapters`/`server_config_values` rows).
- Deploying a template to a specific cluster node, or letting cluster
  placement pick one, is now a first-class, working UI flow instead of a
  backend-only capability with a broken or missing frontend.
