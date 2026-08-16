# Valheim Dedicated Server — GameNode Official template

This directory contains a schema-v2 SteamCMD template for Valheim Dedicated Server and the
schema-v2 `managed-launch` configuration adapter that owns its runtime settings.

## Verified upstream facts

- Dedicated server Steam App ID: `896660`.
- Installation is available through Steam/SteamCMD and anonymous SteamCMD access is supported.
- Windows uses the native `valheim_server.exe` launcher; GameNode launches it directly and never
  executes `start_headless_server.bat`.
- Documented server options used by this template: `-name`, `-port`, `-world`, `-password`,
  `-savedir`, `-public` (`0` or `1`), `-crossplay` (presence-only), and `-saveinterval`
  (seconds, upstream default `1800`).
- Valheim uses the selected UDP port and the next UDP port. The documented default is `2456-2457`.
- Omitting `-crossplay` selects the Steam backend.
- The official guide documents CTRL+C as the correct shutdown method.

Primary references:

- Iron Gate: https://valheim.com/support/a-guide-to-dedicated-servers/
- Valve dedicated server list: https://developer.valvesoftware.com/wiki/Dedicated_Servers_List

Options that upstream documents but this template does not expose (`-logFile`, `-backups`,
`-backupshort`, `-backuplong`, `-instanceid`, `-preset`, `-modifier`, `-setkey`) are omitted
deliberately. Add one only after verifying its exact grammar and acceptance-testing it.

## Base launch versus managed configuration

The template's `platform_launches.windows` entry is the **base launch**: the fixed arguments that
never change per server.

```text
valheim_server.exe -nographics -batchmode -port <SERVER_PORT> -savedir data
```

Everything a user edits afterwards lives in the `valheim-settings` adapter and is applied to the
argument list at start:

| Setting | Binding | Result |
| --- | --- | --- |
| `SERVER_NAME` | `launch-value -name` | `-name` `<value>` |
| `WORLD_NAME` | `launch-value -world` | `-world` `<value>` |
| `SERVER_PASSWORD` | `launch-secret -password` | `-password` `<secret>` |
| `PUBLIC_VISIBILITY` | `launch-value -public` (`1`/`0`) | `-public` `1` or `-public` `0` |
| `CROSSPLAY` | `launch-flag -crossplay` | `-crossplay` when enabled, nothing when disabled |
| `SAVE_INTERVAL_SECONDS` | `launch-value -saveinterval` | `-saveinterval` `<value>` |

Each user value stays exactly one argv element. There is no shell, no string joining, and no
user-supplied argument name.

## Password handling and its operating-system limit

Valheim accepts the server password only as a process argument. GameNode stores it in
`server_config_values`, never returns it from the configuration API, and keeps it out of audit
records, logs, diagnostics, support bundles, provisioning job state, and the persisted registration
snapshot. The value is inserted into the child process argument list at start and nowhere else.

A sufficiently privileged local operating-system user can still read another process's arguments.
That limit comes from the game, not from GameNode, and GameNode does not relax its own redaction
because of it.

Valheim refuses to advertise a public server without a password, so leave `SERVER_PASSWORD` empty
only for a private server that players join directly.

## Other deliberate constraints

- Windows only. Upstream also supports Linux, but GameNode requires each platform launch to be
  independently verified and direct. Do not execute `start_server.sh`.
- `terminate` is only a bounded fallback. It is not claimed to be equivalent to Valheim's
  documented CTRL+C shutdown path.
- `-savedir data` keeps world and permission files under the GameNode server root.
- Automatic SteamCMD update-on-start is not enabled.
- `SERVER_PORT` remains a template variable because it drives GameNode port registration and
  preflight. It is intentionally not a managed configuration binding.

## Validation checklist

1. `go test ./internal/templates` and `go test ./...`.
2. Perform a fresh real SteamCMD install of App ID `896660` on Windows.
3. Verify `valheim_server.exe` exists as a regular file inside the managed root.
4. Verify the resolved argument list without executing the batch wrapper.
5. Verify ports `SERVER_PORT/udp` and `SERVER_PORT+1/udp`.
6. Verify start, console output, player join, world persistence, and shutdown behavior.
7. Independently verify Linux executable/environment/stop behavior before adding Linux to
   `platforms`.
8. Run the frontend helper/build checks.
