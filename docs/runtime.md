# Native runtime (v0.1)

## Monitoring and health

The native runtime exposes identity-verified cumulative CPU time and resident/working-set memory to `internal/monitoring`. Linux uses `/proc`; Windows verifies the process creation key before querying process times and working-set memory. GameNode samples every five seconds by default (`monitoring.sample_interval_seconds`) and retains 300 in-memory samples per server (`monitoring.history_limit`). Health is `healthy` for running sampled processes, `degraded` when their metrics are unavailable, `detached` after verified rediscovery, plus `stopped`, `crashed`, and `unknown`. No network or game-protocol probes are performed.

GameNode starts directly executable native applications from `Executable` plus `Arguments[]`; it does not execute a user-provided shell command. `WorkingDirectory` must exist and the executable must be an existing regular file. Batch files, PowerShell scripts, and shell scripts are intentionally not supported in v0.1 because they require an explicit shell execution mode. Environment variable values are passed only to the child process and are never logged.

`Stop` and `Kill` are intentionally distinct. The `terminate` stop method uses the OS lifecycle path: on Linux it sends `SIGTERM` to the process group and escalates to `SIGKILL`; on Windows it requests `taskkill /PID <pid> /T` and escalates to `/F`. The `stdin_command` stop method writes one configured line to the active process console, waits for the identity-bound process finalizer, and kills after `StopTimeout`. It is unavailable after rediscovery because standard handles cannot safely be reattached. Kill remains immediate. If Windows cannot perform forced tree termination, Kill falls back to terminating the verified root process; orphaned wrapper children are a documented limitation.

### `console_interrupt` (Windows only)

`stdin_command` writes ordinary text to a process's standard input; it cannot produce a real `Ctrl+C`/`Ctrl+Break`. On Windows those are console control events delivered by `GenerateConsoleCtrlEvent`, not bytes on a stream, and `^C` in an imported Pterodactyl/Pelican egg is therefore never treated as a synonym for a stdin stop command. `console_interrupt` is a separate, compiled stop type for processes that document handling Ctrl+C/Ctrl+Break as their graceful-shutdown path (Satisfactory's `FactoryServer.exe` is the first such server).

A `console_interrupt` server is started with `CREATE_NEW_PROCESS_GROUP`, making it the root of its own console process group (group ID equal to its own PID) without allocating it a new console. `Interrupt` verifies PID and `StartKey` and then sends a `CTRL_BREAK_EVENT` scoped to exactly that process group. `CTRL_BREAK_EVENT` is used rather than `CTRL_C_EVENT` because `GenerateConsoleCtrlEvent` can only target a specific process group with `CTRL_BREAK_EVENT`; `CTRL_C_EVENT` can only be broadcast to process group `0`, meaning the entire console — GameNode and every sibling server sharing it. GameNode never sends a group-`0` signal. Most console applications, including Satisfactory's Unreal Engine server, treat a delivered `CTRL_BREAK_EVENT` the same as `Ctrl+C` through the same registered handler.

`GenerateConsoleCtrlEvent` only succeeds for a caller that shares the target's console, which the long-running GameNode process frequently does not (started as a Windows service/Session 0 process, or with no console at all). Rather than have the long-running GameNode process itself call `AttachConsole`/`FreeConsole` — a process-wide, not per-thread, state change that a multi-server, multi-goroutine process cannot safely serialize around its own I/O — `Interrupt` re-execs the same compiled GameNode binary as a disposable, single-purpose helper (`internal/runtime.RunConsoleSignalHelper`, dispatched from the first line of `main()` via an internal environment variable, never an argv flag or externally documented CLI). The helper attaches to the target's console, disables its own default control handler (documented Microsoft guidance for any caller of `GenerateConsoleCtrlEvent`), generates the event, detaches, and exits with a stable code. It is not a second persistent service, not an external script or shell, and it is never a member of the target's process group, so it cannot receive the event it delivers. GameNode's own console attachment is never touched.

Like `stdin_command`, `console_interrupt` requires the matching attached in-memory process instance and waits up to `StopTimeout` before falling back to the existing bounded force-kill path; a timeout fallback is logged but does not itself fail the Stop call, matching every other stop method. A rediscovered process (after a GameNode restart) has no verifiable, safely addressable console in the new GameNode process's lifetime, so `console_interrupt` deliberately falls back to the `terminate` lifecycle for that one stop instead of claiming a graceful interrupt it cannot deliver; GameNode never reattaches a foreign console to make that claim true. `RunConsoleSignalHelper`/`Interrupt` are Windows-only; Linux always returns the stable `ErrConsoleInterruptUnsupported`/`RUNTIME_CONSOLE_INTERRUPT_UNSUPPORTED` outcome rather than emulating the concept.

NeoForge is launched as Java itself, not via its wrapper, so GameNode tracks the actual server PID and owns its stdout, stderr, and stdin pipes. Java argfile tokens beginning with `@` are individual arguments interpreted by Java; they are not shell expansion.

Runtime state is persisted separately from the server definition. It includes PID, an OS process-start key, timestamps, exit status, last error, and the last known state. After restart, GameNode verifies PID and start key before reporting a process as running. If verification fails, it reports `unknown` and will not signal the process.

Console I/O is available for managed starts through native stdin/stdout/stderr pipes and a session bound to the server and process instance. A process rediscovered after GameNode restarts remains running but is deliberately console-detached; it has no synthetic stdin or output attachment. A GameNode restart does not intentionally terminate child processes, but a managed wrapper may exit while its child continues; reliable child reattachment is still limited to process identity verification.

Automatic restart is opt-in per server. A bounded rolling attempt window and cancellable delay apply only after a finalized unexpected non-zero exit. Stop, Kill, Delete, manual Start, and manual Restart cancel a pending delayed restart. Restarted instances use the normal start orchestration and therefore receive fresh process and console identities; pending timers are not restored after GameNode restart.

Port preflight runs after the existing `already running` validation and before runtime-state mutation, Console Session creation, or `Runtime.Start`. Manual restart runs it only after the old process has exited and finalized; auto-restart uses this same normal start path. A confirmed collision prevents the new launch, records `last_error`, and is not a process crash, so it does not increase `crash_count` or create a recursive restart loop. Pending auto-restart state is cleared before the attempt. OS availability probes are best effort: a successful temporary bind check cannot guarantee that the game process will later bind the port because of TOCTOU.

Complete 6C runtime test-matrix validation and Windows E2E remain pending on an environment that permits executable test binaries.
# Container runtime (v0.3)

Container servers remain ordinary `servers.Service` workloads alongside Native
servers. Docker is controlled only through its Engine API. A container mounts
only its GameNode server root at `/home/container`, uses bridge networking, and
receives typed CPU/RAM limits and registered host-to-container ports.

Every operation verifies managed/server/generation/token labels stored in the
Container StartKey; foreign or stale containers are never adopted. Non-TTY
Engine attach feeds the existing ConsoleManager. Image Pull is explicit;
Start never pulls or updates an image. Availability is queried from the Engine,
and transient pull state is cleared on GameNode restart. Container memory is
Engine/cgroup usage, not a claim of native RSS equivalence.
