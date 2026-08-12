# Native runtime (v0.1)

## Monitoring and health

The native runtime exposes identity-verified cumulative CPU time and resident/working-set memory to `internal/monitoring`. Linux uses `/proc`; Windows verifies the process creation key before querying process times and working-set memory. GameNode samples every five seconds by default (`monitoring.sample_interval_seconds`) and retains 300 in-memory samples per server (`monitoring.history_limit`). Health is `healthy` for running sampled processes, `degraded` when their metrics are unavailable, `detached` after verified rediscovery, plus `stopped`, `crashed`, and `unknown`. No network or game-protocol probes are performed.

GameNode starts directly executable native applications from `Executable` plus `Arguments[]`; it does not execute a user-provided shell command. `WorkingDirectory` must exist and the executable must be an existing regular file. Batch files, PowerShell scripts, and shell scripts are intentionally not supported in v0.1 because they require an explicit shell execution mode. Environment variable values are passed only to the child process and are never logged.

`Stop` and `Kill` are intentionally distinct. The `terminate` stop method uses the OS lifecycle path: on Linux it sends `SIGTERM` to the process group and escalates to `SIGKILL`; on Windows it requests `taskkill /PID <pid> /T` and escalates to `/F`. The `stdin_command` stop method writes one configured line to the active process console, waits for the identity-bound process finalizer, and kills after `StopTimeout`. It is unavailable after rediscovery because standard handles cannot safely be reattached. Kill remains immediate. If Windows cannot perform forced tree termination, Kill falls back to terminating the verified root process; orphaned wrapper children are a documented limitation.

NeoForge is launched as Java itself, not via its wrapper, so GameNode tracks the actual server PID and owns its stdout, stderr, and stdin pipes. Java argfile tokens beginning with `@` are individual arguments interpreted by Java; they are not shell expansion.

Runtime state is persisted separately from the server definition. It includes PID, an OS process-start key, timestamps, exit status, last error, and the last known state. After restart, GameNode verifies PID and start key before reporting a process as running. If verification fails, it reports `unknown` and will not signal the process.

Console I/O is available for managed starts through native stdin/stdout/stderr pipes and a session bound to the server and process instance. A process rediscovered after GameNode restarts remains running but is deliberately console-detached; it has no synthetic stdin or output attachment. A GameNode restart does not intentionally terminate child processes, but a managed wrapper may exit while its child continues; reliable child reattachment is still limited to process identity verification.

Automatic restart is opt-in per server. A bounded rolling attempt window and cancellable delay apply only after a finalized unexpected non-zero exit. Stop, Kill, Delete, manual Start, and manual Restart cancel a pending delayed restart. Restarted instances use the normal start orchestration and therefore receive fresh process and console identities; pending timers are not restored after GameNode restart.

Port preflight runs after the existing `already running` validation and before runtime-state mutation, Console Session creation, or `Runtime.Start`. Manual restart runs it only after the old process has exited and finalized; auto-restart uses this same normal start path. A confirmed collision prevents the new launch, records `last_error`, and is not a process crash, so it does not increase `crash_count` or create a recursive restart loop. Pending auto-restart state is cleared before the attempt. OS availability probes are best effort: a successful temporary bind check cannot guarantee that the game process will later bind the port because of TOCTOU.

Complete 6C runtime test-matrix validation and Windows E2E remain pending on an environment that permits executable test binaries.
