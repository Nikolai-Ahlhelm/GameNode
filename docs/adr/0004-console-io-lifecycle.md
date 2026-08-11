# ADR 0004: Console I/O lifecycle and detached rediscovery

## Problem

Console I/O belongs to the process launched by the current GameNode instance. After a GameNode restart, the process may survive but its original stdin/stdout/stderr pipes cannot be recovered safely.

## Decision

The runtime owns OS pipes and forwards output into a transport-independent ConsoleManager. Each started process receives a fresh in-memory console session with separate stdout/stderr events, a 1,000-line ring buffer, bounded subscriber queues, and a validated stdin writer. WebSockets are subscribers only.

Slow subscribers never block output capture: each has a fixed queue and is disconnected after queue overflow. Console contents are not persisted or logged. After process exit or lifecycle stop, the manager closes stdin and notifies subscribers. A rediscovered process is explicitly `detached`: it remains visible as running but has no history, output, or input until restarted through GameNode.

## Consequences

No pseudo-terminal support, terminal emulation, or pipe reattachment is attempted. A future RBAC layer can map Console.View and Console.Send to manager subscription/input methods.
