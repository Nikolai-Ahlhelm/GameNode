# 7 Days to Die Official template

This game directory is one review and release unit:

- `template.json` defines SteamCMD App ID 294420, native platform launches, variables, ports, and the adapter reference.
- `serverconfig.adapter.json` maps approved typed fields to direct `<property name="..." value="..."/>` entries in `serverconfig.xml`.
- `fixtures/serverconfig.example.xml` is a small parser/writer regression fixture, not a file copied into installations.

Increment the template version when installation, launch, defaults, ports, or configuration behavior changes. Increment the adapter version when its target or field mapping changes. Existing servers retain their persisted adapter snapshot.
