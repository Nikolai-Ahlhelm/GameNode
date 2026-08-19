# ADR 0007: Container runtime ownership and Docker Engine API boundary

## Problem

GameNode needs a second runtime without shelling out to Docker or adopting
foreign containers after restart.

## Options

Use the Docker CLI, use the Docker Engine API, match containers by name, or
verify GameNode-owned metadata.

## Trade-offs

The Engine API adds a small client boundary and requires a local Docker daemon,
but avoids shell parsing and supports typed requests. Labels alone are not a
secret, so ownership also needs a durable random token and instance generation.

## Decision

GameNode keeps Native and Container runtimes separate behind `servers.Service`.
Container control uses the Docker Engine API only. Each created container has
managed, server-ID, generation, and durable ownership-token labels. Its
persisted StartKey carries the verified identity; name-only adoption is never
allowed.

## Consequences

Foreign or stale containers are rejected for status, lifecycle, metrics, and
console attach. Docker remains a host dependency; GameNode exposes no generic
Docker API, CLI, host mounts, privileged mode, socket mounts, or HostConfig.
