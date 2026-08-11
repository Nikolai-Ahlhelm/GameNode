# ADR 0003: Native runtime, process identity, and lifecycle semantics

## Problem

Milestone 2 must manage arbitrary native applications without shell injection, distinguish graceful stop from kill, and avoid acting on a reused PID after a GameNode restart.

## Options

Track a PID only; launch shell command strings; or use direct executable/argument launching with an OS-specific process identity and process-tree handling.

## Trade-offs

PID-only recovery is unsafe because PIDs can be reused. Direct execution does not support arbitrary shell syntax, which is intentional. Native child-process handling differs between Windows and Linux and cannot be hidden by pretending it is identical.

## Decision

The persisted server definition contains an executable path, an argument list, a working directory, and an explicit environment map. The runtime never invokes `cmd.exe /c` or `sh -c` for server definitions.

Linux launches each process in a new process group. Stop sends `SIGTERM` to that group and waits up to `StopTimeout`; it escalates to `SIGKILL` on timeout. Kill immediately sends `SIGKILL` to the group.

Windows uses direct process creation. Lifecycle tree operations invoke the trusted system utility `taskkill.exe` with separately supplied `/PID`, `/T`, and, for Kill, `/F` arguments. No user-provided value is evaluated by a shell. If forced tree termination is unavailable, Kill falls back to `TerminateProcess` for the verified root process and records the tree limitation. Stop requests a tree termination and waits up to `StopTimeout`; Kill forces tree termination.

Runtime state persists PID and a platform-specific process-start key. Rediscovery only treats a process as running when both match. Linux reads `/proc/<pid>/stat`; Windows reads the process creation time through the Windows API. A process whose identity cannot be verified is `unknown`, never controllable.

## Consequences

Linux can signal process groups directly. Windows child processes are addressed through `taskkill /T`; job objects are deliberately not used because a job owned by GameNode would complicate process survival across a GameNode restart. Milestone 2 accepts directly executable native programs only; batch, PowerShell, and shell wrappers require a future explicit execution mode. Reliable child reattachment and console I/O reattachment are explicitly deferred. Stdin-command stops are deferred to the console milestone.
