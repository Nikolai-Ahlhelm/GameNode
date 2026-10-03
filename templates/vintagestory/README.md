# Vintage Story Official template

Installer type `vintagestory` (compiled in `internal/vintagestory`). The user chooses one exact version; the download source is fixed in code and verified by checksum. Launch resolver `vintagestory` runs `dotnet VintagestoryServer.dll --dataPath data --port N`. The `serverconfig.json` adapter is post-start-only. See `docs/adr/0014-vintage-story-and-hytale.md`.
