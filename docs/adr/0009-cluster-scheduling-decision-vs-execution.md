# 0009. Cluster Scheduling: Decision vs. Execution boundary (v0.6)

> **Update (v0.5B/v0.6 execution):** v0.5B Remote Server Management has since
> been implemented. The `requires_v0.5b` execution value described below
> was renamed to `remote_executable` to reflect that a caller now CAN act
> on a remote selection through `POST /api/v1/remote-nodes/{id}/servers` -
> this is a status/capability label update. The placement decision endpoint
> remains read-only; the separate explicit
> `POST /api/v1/cluster/placement/execute` endpoint recomputes the decision
> and delegates creation to the selected node's normal create path.
> `internal/placement` remains pure and never executes anything itself.

## Problem

v0.6 adds multi-node placement, capacity-awareness, and a deterministic scheduling algorithm across every node this installation knows about. Remote creation is now available through v0.5B's authenticated Node API; this ADR keeps it behind an explicit execution endpoint rather than making a read-only decision request mutate state.

## Options considered

1. **Implement remote server creation as part of "scheduling execution".** Rejected: this is the entire v0.5B milestone (remote Server Create/Start/Stop/Kill through the Node API), not a small addition. Building it here would be undocumented, unreviewed scope creep and would duplicate/fork `servers.Service`'s lifecycle authority onto a second, remote-facing path.
2. **Only ever schedule for the local node, and reject any request that would place a server on a Remote Node.** Rejected: this throws away the actual product value of a placement decision (helping an operator/future automation pick a node among several) and makes the feature a no-op wrapper around "just call `servers.Service.Create`".
3. **Compute the placement decision for every eligible node (local and remote), but stop at the decision.** The API returns which node is the best fit and never itself performs the creation - not even for the local node. A caller (operator, and later scripted automation) acts on the decision through the ordinary, already-reviewed `servers.Service`/provisioning create path. Chosen.

## Decision

- **`internal/placement`** is a new, pure, deterministic package: `Decide(Request) Decision` takes an already-built list of `NodeCandidate` values (capacity, health, capability, tenant scope already resolved by the caller) and returns a `Decision` with zero I/O, zero side effects, and zero dependency on wall-clock time or map iteration order. This is what makes the placement algorithm exactly unit-testable with fixed inputs (see `internal/placement/placement_test.go`) - the same reason `internal/scheduler` (the unrelated v0.4 LOCAL restart scheduler) keeps its trigger-evaluation logic separate from the DB/clock plumbing around it.
- **`internal/placement` is not `internal/scheduler`.** `internal/scheduler` is the v0.4 typed daily/weekly LOCAL restart scheduler; it only ever calls `servers.Service.Restart` on an existing local server on a timer and has no concept of nodes, placement, or capacity. `internal/placement` never touches restart schedules and never calls anything in `internal/scheduler`. Neither package imports the other.
- **Every decision carries an `Execution` field**: `local_only` when the selected node is this installation, or `remote_executable` when the selected node is a Remote Node. These are contract metadata only; the pure placement package never executes a create.
- **The placement API (`POST /api/v1/cluster/placement`) never creates, starts, stops, or otherwise mutates any server, on the local node or a remote one.** This holds even when the decision is `local_only`. A caller who wants to act on a `local_only` decision issues a normal `POST /api/v1/servers` (or a provisioning request) afterward, informed by the decision - exactly the boundary `AGENTS.md` §4 draws ("Templates and provisioning must end by creating an ordinary `servers.Server`... Do not create a second... lifecycle").
- **Capacity is read-only and additive.** `placement.LocalCandidate` counts this node's servers via `servers.Service.List`; reachable remote nodes use the bounded v0.5B server listing and fall back to unknown capacity when unavailable. A capacity-unknown candidate remains eligible but ranks below verified spare capacity.
- **RBAC and tenant isolation are enforced by the API layer, not by `internal/placement`.** Two new permissions, `Cluster.View` (read capacity/candidates) and `Cluster.Schedule` (compute a decision), both accept `global` and `tenant` scope only (no `server` scope - a server does not exist yet at decision time, the same reasoning `Server.Create` already uses). A placement request is always evaluated against the requested tenant's scope before the tenant is even confirmed to exist, so a caller without the permission learns nothing about whether the tenant exists.
- **Audit records the decision, not the polling.** One `cluster.placement_decide` audit event is recorded per placement request, `Success` when a node was selected and `Failure` when none was eligible - never for the read-only capacity listing (`GET /api/v1/cluster/capacity`), matching the existing "no audit for routine reads/health polls" convention (`AGENTS.md` §9, and v0.5A's node heartbeat).

## Trade-offs

Stopping at the decision means v0.6 does not, by itself, make multi-node placement "do" anything for a Remote Node target - an operator still manually creates the server there once v0.5B exists (or continues to create it directly on the intended node today, without this feature). That is accepted as the correct size for a foundation milestone: it gives an honest, capacity-aware, tenant-isolated, auditable recommendation across every known node today, without inventing a second remote-mutation surface ahead of its own reviewed milestone. The capacity model itself is intentionally simple (a single fixed `DefaultMaxServersPerNode` constant for the local node, no configuration UI, no per-tenant capacity split) because no capacity configuration concept exists anywhere in the product yet; making it operator-configurable is future work, not a redesign of `internal/placement`'s decision algorithm.

## Consequences

- The explicit placement execution endpoint can create through the existing local service or the authenticated v0.5B Node API without changing `internal/placement.Decide`.
- A future capacity model (operator-configurable max servers per node, real remote capacity via a v0.5B/v0.5C read call, resource-based scoring instead of server-count scoring) can replace `placement.LocalCandidate`/`placement.RemoteCandidates` and `DefaultMaxServersPerNode` without changing `Decide`'s algorithm or its test contract.
- Server migration, failover, and controller election remain explicitly out of scope; nothing in this package assumes a server can move between nodes.
