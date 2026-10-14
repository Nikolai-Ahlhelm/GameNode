# ADR 0015: Minecraft Java startup management

## Status

Accepted for the Minecraft Java startup configuration slice.

## Decision

Minecraft startup settings remain specific to `internal/minecraft`. GameNode resolves Java, memory, distribution, and argument files into an executable plus a structured argument array. Native settings are copied to the ordinary `servers.Server` launch record; Container settings become the ordinary closed `ContainerConfig.Command`. Both continue through `servers.Service` and the existing Native/Container runtime implementations.

NeoForge and Forge argument files are discovered beneath the server root and validated before launch. The batch scripts are never parsed or executed. The optional `user_jvm_args.txt` is read through the server filesystem sandbox, parsed into individual JVM arguments, and its Xms/Xmx tokens are replaced by the dashboard's typed heap fields. Paper, Vanilla, and Fabric use the installed `server.jar`.

Container Java is resolved inside the selected image. The container's working directory is the existing fixed `/home/container` mount; stdin remains open through the existing Docker attach channel. No RCON service or separate process manager is added.

Settings are stored in `minecraft_startup_configurations` and are editable only while stopped. Resolved launch arguments are persisted on the ordinary server record so existing startup, rediscovery, console, and lifecycle paths remain authoritative.

## Consequences

- Existing server directories and JVM argument files are preserved.
- Runtime launch stays shell-free and uses direct argv.
- Heap settings are checked against Container memory limits and custom JVM arguments cannot duplicate managed heap flags.
- This slice does not add Paper provisioning/version downloads. Paper support applies to an existing `server.jar` installation.
- Java compatibility against the server's exact Minecraft version remains advisory; the startup editor verifies executable presence on Native hosts, while Container resolution is performed by the image.
- The existing local scheduler is still daily/weekly. Its Minecraft adapter sends `save-all flush` and requires a fresh save acknowledgement before calling the normal lifecycle restart. A detached console fails safely.
- Configurable restart warning milestones and repeating MOTDs are stored in `minecraft_message_settings`; delivery keys and MOTD last-send timestamps persist in `minecraft_message_deliveries`. Text is emitted as escaped Minecraft JSON components through attached console stdin.
- Restart warning offsets and MOTD intervals are independent. Each message supports a template, chat/title/subtitle channel, Minecraft color, and bold/italic/underline/strike-through formatting with a dashboard preview.
- Countdown execution history, cron/interval restart recurrence, manual schedule execution/history endpoints, and startup-completion verification remain unimplemented.
