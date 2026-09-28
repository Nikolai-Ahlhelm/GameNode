# RuneScape: Dragonwilds Dedicated Server — GameNode template

Files:

- `template.json` — schema-v2 SteamCMD/direct-launch template (App ID 4019830, Windows and Linux)
- `dragonwilds-windows.adapter.json` / `dragonwilds-linux.adapter.json` — reviewed configuration adapters, one per host OS
- `fixtures/DedicatedServer.example.ini` — parser/writer regression fixture (test data only)

## Configuration

Both adapters use the compiled `ini-section-key-values` format on `[/Script/Dominion.DedicatedServerSettings]` and are restricted with `platforms`, because the config path differs per OS (`WindowsServer/` vs `LinuxServer/` below `RSDragonwilds/Saved/Config/`). GameNode manages `OwnerId`, `ServerName`, `DefaultWorldName`, `AdminPassword` (secret), and `WorldPassword` (secret, optional). Everything else in the file, including the `ServerGuid` and admin history the server writes itself, is preserved.

The server creates `DedicatedServer.ini` on its first run and exits without a valid `OwnerId`, so the adapters are `post_start_only`: provisioning does not invent the file. Workflow: provision, start once, stop, fill in the values in the server's Configuration tab, start again. The server overwrites the file while running, so edit only while stopped.

The editor updates existing keys only; it does not create a missing section or key. If a future server build omits one of the five keys from the generated file, the edit is rejected with a parse error rather than guessed.

Length limits and the Owner ID format are not documented by an official source, so validation only enforces generous bounds. Servers provisioned from template 1.0.0 have no adapter and are not migrated.

## Launch executable

The template launches `RSDragonwildsServer.exe` (Windows) / `RSDragonwildsServer.sh` (Linux) at the server root, matching the `launch` entries in Valve's own app manifest for App ID 4019830 (`steamcmd +login anonymous +app_info_print 4019830`), not a guessed path inside the game's internal Unreal folder. Template 1.0.0/1.1.0 guessed `RSDragonwilds.exe` and a nested `RSDragonwilds/Binaries/Linux/...` path, neither of which exists in the depot; both failed installation validation with `TEMPLATE_EXPECTED_FILE_INVALID` after a fully successful SteamCMD download. 1.2.0 fixes this. A server provisioned from 1.0.0/1.1.0 has the wrong executable pinned and must be reprovisioned; GameNode never migrates an existing server's launch.
