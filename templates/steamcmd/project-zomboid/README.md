# Project Zomboid Official template

This directory owns all reviewed GameNode product data for Project Zomboid.
Template schema v2 installs the Dedicated Server with fixed Steam App ID `380870`,
anonymous login, and validation.

The current Windows depot launches through `StartServer64.bat`, but GameNode does
not execute it. The reviewed `ProjectZomboid64.json` and real depot contents define
the equivalent direct process: bundled `jre64/bin/java.exe`, fixed JVM options,
the relative Windows classpath, and `zombie.network.GameServer`. `-cachedir=.`
keeps generated configuration, saves, logs, and credentials below the managed
server root. The server port is the only editable launch value. Graceful stop is
the bounded stdin command `quit`.

Windows was real-accepted on 2026-08-12 against Steam build `24574884`: anonymous
install/validate, normal GameNode server creation, Official `1.0.0` provenance,
ports, native start, first-boot console interaction, `SERVER STARTED`, save, and
clean exit code 0. Linux is not declared until its native launcher has the same
independent acceptance.

Template `1.1.0` added the versioned `project-zomboid-server-ini` adapter. Current
template `2.0.0` preserves that launch/adapter behavior and adds the hardened v2
artifact, Requirements, port-purpose, help, and UI metadata contract. Project
Zomboid creates `Server/gamenode.ini` only on first start, so the adapter is
persisted during provisioning but remains read-only/pending until that file
exists. It then exposes only the declared flat settings: public name and
description, password, public/open visibility, player limit, PVP, pause-when-empty,
save interval, and startup backups. GameNode preserves comments, ordering, line
endings, and unknown settings while changing only those declared keys.

`SandboxVars.lua` remains deliberately unmanaged. Lua is executable syntax, not
INI data, and must not be handled by generic substitution or remote parser code.
The example INI in `fixtures/` documents the supported upstream shape; it is not
copied into a server.
