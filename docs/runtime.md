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

# Container-backed Egg provisioning (v0.4)

Pelican/Pterodactyl Egg imports expose separate Native and Container compatibility.
Container provisioning is never an implicit fallback: the request must select
`runtime_type: container`, choose a declared image allowed by the node policy, and
use an available Docker Engine. GameNode explicitly pulls the selected game image
and the normalized installer image before installation; an existing server's image
and optional digest remain pinned.

The Egg installation script runs only in a short-lived unprivileged installer
container. Its command is a fixed allowlisted shell entrypoint plus `-lc` and the
bounded normalized script. The only persistent mount is the validated server root at
`/home/container`. No Docker CLI, host shell, daemon socket, host network/PID/IPC,
devices, capabilities, arbitrary mounts, or registry credentials are available.
Memory, CPU, PIDs, tmpfs, output, timeout, cancellation, and cleanup are bounded.
Installer cleanup removes only the ephemeral container; files written below the
persistent root are retained and reported through `files_may_remain` on failure.

The registered server stores an immutable normalized Egg snapshot containing
provenance/hash/version, image/digest, startup template/shell, variable sensitivity,
host/container ports, resource limits, and compiled configuration operations. At
start, only declared variables and the fixed `/home/container` `SERVER_ROOT` value
expand the startup template. The host environment, live catalog, generic regex/eval
configuration, and arbitrary Engine flags are never consulted.
# Scheduled restart lifecycle

Scheduled restarts are local configuration, not a second runtime. At startup
GameNode loads enabled rows from SQLite and registers only the next future
occurrence. If the process was offline at the configured time, that occurrence
is skipped. Before firing, the scheduler re-reads the schedule and confirms it
still exists and is enabled, then calls `servers.Service.Restart(serverID)`.
That service's existing per-server lock, running-state check, stop method,
identity checks, finalization, and normal start path remain authoritative. A
stopped server is therefore not implicitly started; the failed or ineligible
attempt is logged and audited as a scheduled restart.

Daily and weekly times are interpreted in the schedule's stored IANA timezone.
Nonexistent local times during spring DST transitions are skipped. Ambiguous
fall-back times are resolved deterministically and guarded so one occurrence
cannot execute twice within one GameNode process. Editing a schedule replaces
its timer immediately; disabling or deleting it cancels the timer. Server
deletion cascades schedule rows. Shutdown cancels timers and waits for their
bounded scheduler goroutines without touching a runtime directly.

`internal/scheduler` is exclusively this local, single-server restart
scheduler. It is unrelated to, and untouched by, v0.6's cluster placement
DECISION engine (`internal/placement`) or v0.5B/v0.5C's remote server
forwarding (`internal/api/node_servers.go`, `internal/api/remoteservers.go`)
- there is no shared code, no shared concept of "schedule," and no plan to
merge them.

# Remote server lifecycle forwarding (v0.5B/v0.5C)

A remote server's runtime is never simulated or duplicated locally. Every
lifecycle call the controller makes against an enrolled Remote Node
(`internal/remote.Client.StartServer`/`StopServer`/`RestartServer`/
`KillServer`) is forwarded, unmodified, to that node's own
`internal/servers.Service` through the machine-authenticated Node API
(`internal/api/node_servers.go`) - the exact same lock, running-state check,
stop method, identity check, and finalization path a local browser session
on that node would drive. The controller's own database never gains a
row that claims to be a remote server's authoritative runtime state; every
read is a bounded, on-demand call, and a failed/unreachable call is
reported as a transport error (`node_unreachable`, etc.), never presented as
"the server stopped."

Remote console uses a bounded JSON WebSocket relay with polling fallback:
`console.Session.Snapshot()` returns the same bounded 1,000-event ring
buffer the local WebSocket console already uses, so there is no additional
buffering, background goroutine, or long-lived connection introduced by the
remote path. Remote files call the exact same
`internal/filesystem.Service` sandbox as local files, rooted at the target
server's working directory on the node actually running it - never a
controller-side filesystem operation. See
`docs/adr/0010-remote-server-lifecycle-forwarding.md` and
`docs/adr/0011-remote-operational-hardening.md` for the full decision
record.
