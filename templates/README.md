# Official GameNode templates

The current Official Template contract is schema v2. Start with
[`docs/templates.md`](../docs/templates.md), which documents every field, the
security model, a complete example, validation codes, and the contribution
checklist. Machine-readable IDE aids live in `schema/template.schema.json` and
`schema/catalog.schema.json`; backend validation remains authoritative.

This directory is the source of truth for the Official Game Library. GameNode reads
`catalog.json` from the repository's stable `main` branch through GitHub Raw and then
fetches only the relative JSON files listed by that manifest.

Quick start:

1. Copy the closest reviewed `template.json` into one game directory below the appropriate category.
2. Choose a stable unique ID and define `steamcmd` or `existing-files`.
3. Add an explicit launch for every platform, then typed variables, ports,
   expected files, Requirements, and presentation metadata.
4. Add or update its entry in `catalog.json`; keep IDs stable and increment the
   template version whenever behavior changes.
5. Run `go test ./internal/templates`, `go test ./...`, and the frontend checks.
6. Commit the JSON and manifest together and submit them for review. After merge to
   `main`, new library refreshes use the new version. GitHub Raw caches can take a
   short time to converge.

Official templates are declarative, untrusted network input. They cannot contain
shell commands, script hooks, arbitrary download URLs, containers, or host paths.
Only known native installers and resolvers are accepted. Version updates affect new
creates only and never migrate existing servers.

The catalog remains schema v1 and intentionally has no hashes or signatures; it
can reference template schema v1 or v2 during the compatibility transition. The HTTPS fixed-origin
transport, strict schemas, bounded responses, validation, and local last-good cache
are the current trust boundary.

## Official SteamCMD template checklist

1. Confirm the dedicated server App ID and anonymous Steam login.
2. Confirm each supported platform and its exact installed executable.
3. Add one explicit `platform_launches` entry per declared platform.
4. Keep `arguments` as a JSON string array; placeholders may reference declared variables only.
5. Choose `terminate`, or a documented bounded `stdin_command`; never add a stop script.
6. Add only verified ports, using an integer variable when users may change a port.
7. Validate the JSON and catalog with `go test ./internal/templates`.
8. Add/update the catalog entry and bump the template version for executable, arguments, defaults, ports, or installer behavior.
9. Run the backend and frontend suites.
10. When practical, perform an opt-in real provision/start/stop smoke; do not make multi-gigabyte downloads part of CI.

The SteamCMD subset permits only a positive catalog-owned App ID, anonymous login,
the validation boolean, and the existing constrained beta-branch mechanism. It does
not permit credentials, Steam Guard, Workshop, arbitrary commands/flags, download
URLs, scripts, update-before-start, or config-file generation.

Current SteamCMD game directories are `steamcmd/7-days-to-die/`,
`steamcmd/eco/`, `steamcmd/palworld/`, `steamcmd/project-zomboid/`, and
`steamcmd/satisfactory/`. Project Zomboid includes a same-directory, post-start
INI adapter. Its generated Lua configuration remains unmanaged; remote template
data must never introduce a generic parser or executable configuration language.

## Per-game configuration adapters

All remote product data for one game stays together:

```text
steamcmd/7-days-to-die/
├── template.json
├── serverconfig.adapter.json
├── README.md
└── fixtures/serverconfig.example.xml
```

`template.json` may reference adapter JSON by basename only. The reference cannot use a URL, subdirectory, absolute path, drive, UNC path, or traversal. GameNode fetches it relative to the template directory through the same fixed GitHub Raw origin, validates it, and caches it beside the template.

Schema v1 supports the compiled formats `xml-properties`, `ini-key-values`, and
`section-tuple-key-values`. The latter selects one exact `section` and
`container_property` whose value is a parenthesized typed key/value tuple. It
preserves unknown properties and is not a generic Unreal configuration parser.
An optional reviewed `initialization` with mode `seed-from-file` may initialize
a missing target from another bounded, validated server-root-relative file;
existing targets are never seed-overwritten.
The INI implementation accepts a flat, sectionless `key=value` document, rejects
malformed/duplicate/missing managed keys, and preserves comments, ordering, line
endings, and all unknown keys. `post_start_only` adapters may expose validated
configuration-only fields and stay pending until the game generates their file.
Definitions cannot contain XPath, regular expressions, scripts, hooks, parser
code, external URLs, or escaping target paths. Fixtures document expected
upstream shape but are never copied or executed.
