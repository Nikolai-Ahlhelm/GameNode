# Palworld Dedicated Server — GameNode template

Files:

- `template.json` — schema-v2 SteamCMD/direct-launch template
- `palworld-settings.adapter.json` — reviewed configuration adapter descriptor
- `fixtures/PalWorldSettings.example.ini` — parser/writer regression fixture

## Compiled adapter format

Palworld does not use a normal `key=value` INI layout for these settings. The target contains a section and a single `OptionSettings=(...)` tuple. The descriptor therefore uses GameNode's compiled, reusable `section-tuple-key-values` format and declaratively selects `/Script/Pal.PalGameWorldSettings` plus `OptionSettings`.

The parser has no Palworld-specific keys. It modifies only descriptor-approved typed properties, preserves unknown tuple properties and other sections, and never treats configuration data as a shell, script, expression, or regex patch. This format is suitable only for the same section/container/tuple file shape; it does not imply generic Unreal Engine compatibility.

If `Pal/Saved/Config/WindowsServer/PalWorldSettings.ini` does not exist, the declared `seed-from-file` initialization reads and validates `DefaultPalWorldSettings.ini`, creates target parents inside the server root, and atomically creates the patched target before registration. Existing targets are patched and are never overwritten from the seed.

The fixture is test data only and must never be copied into a production installation.
