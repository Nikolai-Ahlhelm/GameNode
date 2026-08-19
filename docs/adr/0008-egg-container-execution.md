# ADR 0008: Controlled Container execution for imported Eggs

## Problem

Pelican/Pterodactyl Eggs commonly describe installation and startup as shell text
inside a container. GameNode must support useful Container-backed imports without
turning an untrusted Egg into host code execution or a second server lifecycle.

## Options

1. Execute the Egg script on the GameNode host.
2. Add a generic host script/plugin engine.
3. Treat all Egg scripts as metadata and support no Container-backed imports.
4. Execute a normalized, bounded plan in a short-lived unprivileged Docker
   container through the existing Engine API.

## Decision

Choose option 4. Native and Container compatibility remain separate, and Container
provisioning requires an explicit runtime selection. The analyzer retains only strict
declared image references, an allowlisted shell entrypoint, bounded installation and
startup text, declared-variable placeholders, fixed resource defaults, and compiled
properties/key-value/JSON configuration operations. Unsupported required semantics
are findings, not an invitation to execute arbitrary text.

The installer container is created and removed through the Docker Engine API. It has
only the validated persistent server root mounted at `/home/container`; no Docker
socket, host network/PID/IPC, devices, capabilities, arbitrary mounts, privileged
mode, or registry credentials are allowed. Pull is explicit, registry names are
administrator-allowlisted, resources/output/time/cancellation are bounded, and
cleanup never recursively removes the persistent root. A normal `servers.Server`
with `runtime_type: container` is created only after installation validation and
transactional registration. `servers.Service` remains lifecycle authority.

## Trade-offs

This supports common safe Eggs while rejecting arbitrary installer ecosystems,
custom package managers, generic regex/eval config, and scripts that depend on
unsupported container semantics. A tag may resolve to a different image on a later
manual pull, so a digest is recorded when the Engine exposes one and existing server
configuration is never automatically migrated. A failed install can leave files;
the job reports `files_may_remain` and uses the existing bounded owner/admin
registration-recovery flow rather than unsafe automatic deletion.

## Consequences

- The runtime boundary stays Docker Engine API-only and the host has no Egg shell
  execution path.
- Provisioning reuses the existing persisted jobs, RBAC/CSRF/audit, output limits,
  target reservation, cancellation, and registration semantics.
- Container server snapshots pin provenance, image/digest, startup, sensitivity,
  ports, resources, and config operations; catalog refreshes affect future creates
  only.
- Remote Nodes, scheduling, automatic updates, registry authentication, and generic
  host script execution remain outside this milestone.
