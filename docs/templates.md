# GameNode template contract

## Overview

A GameNode template is declarative, untrusted data that describes how a known native game server can be installed, validated, launched, stopped, and presented. A template is not a script, plugin, container definition, or command language. The backend parses and validates every Official file again after download; the JSON Schemas in `templates/schema/` are contributor and IDE aids, not the security boundary.

Schema v2 extends v1 without changing the native runtime. Existing v1 catalog/cache data remains readable. Existing servers retain their already-resolved executable, argument array, environment, ports, stop behavior, adapter snapshots, template ID/source/version, and are never migrated when a catalog template changes.

## Security model

Launches always become one executable plus `arguments[]`. GameNode never joins those values into a shell command. `cmd.exe`, PowerShell, `sh`, `bash`, script hooks, pipes, redirects, command substitution, arbitrary download URLs, Docker/container fields, generic package managers, and user-controlled SteamCMD flags are outside the contract.

Executable, working-directory, expected-file, and configuration metadata paths are server-root-relative. Absolute Windows/Linux paths, drive paths, UNC paths, traversal, NUL/newline data, and symlink targets outside the root fail validation. Shell metacharacters inside an ordinary argument are harmless argv data and are not interpreted. A sensitive variable may be used only where the compiled backend explicitly permits it; Official templates cannot place it in executable paths or arguments. Sensitive values are masked from APIs, audit, logs, installer output, job history, diagnostics, and support bundles.

Official templates use strict JSON decoding: unknown fields fail. Pelican/Pterodactyl Eggs retain their bounded tolerant source parser, then normalize into the existing strict GameNode domain model with compatibility findings. Raw Egg JSON and scripts are not persisted or executed.

## Top-level schema

| Field | Meaning |
| --- | --- |
| `schema_version` | `2` for this contract; v1 remains readable. |
| `id` | Stable lowercase identifier. Never reuse an ID for another product. |
| `name`, `description`, `category`, `tags`, `icon` | Catalog and presentation metadata. |
| `version` | Semantic template version. Bump for installer, launch, variable/default, port, artifact, adapter, or behavior changes. |
| `minimum_gamenode_version` | Minimum release understood by this template. Development builds are treated as current enough. |
| `platforms` | Explicit `windows` and/or `linux` support. Every SteamCMD platform requires a launch entry. |
| `installer` | One whitelisted installer definition. |
| `launch` / `platform_launches` | A resolver-based launch or explicit platform launches. |
| `variables` | Typed, validated provisioning inputs. |
| `ports` | Port rows derived from constants or integer variables. |
| `expected_files` | Required/optional post-install artifacts. Required in schema v2. |
| `config_files` | Documentation-only path/format hints. This field does not edit files. |
| `requirements` | Typed hard checks and informational hints. |
| `help`, `known_limitations` | Operator guidance without executable behavior. |
| `configuration` | References reviewed, same-directory configuration adapters implemented by compiled GameNode code. |
| `compatibility` | General understanding status and stable findings. |

`source_type`, `source_identifier`, `source_format_version`, `source_metadata`, and `read_only` establish provenance. Official files use `source_type: official` and `read_only: true`; catalog metadata must match the downloaded template.

## Installer types

### `steamcmd`

The nested plan accepts a positive catalog-owned `app_id`, `login_mode: anonymous`, `validate`, `platform` (`native`, `windows`, or `linux`), `install_target: server_root`, and an optional constrained beta-branch variable. Credentials, beta passwords, Steam Guard, Workshop, arbitrary flags, URLs, mirrors, and update-on-start are rejected. GameNode uses its fixed Valve HTTPS sources and structured `exec.CommandContext` invocation.

### `existing-files`

No download occurs. The operator supplies an existing installation. It can use reviewed direct platform launches or a compiled resolver. The legacy v1 value `existing` remains readable for the shipped NeoForge template but new templates use `existing-files`.

### Resolvers

`java` discovers Java only through `JAVA_HOME/bin/java` or `PATH` and then uses the declared argv list. It does not install Java or accept a template-selected host executable. `neoforge` is a stricter special case: it reads bounded local `run.bat`/`run.sh`, verifies that the launcher is the expected direct Java plus local `@argfiles` form, validates the NeoForge argfile, replaces free-form user JVM arguments with typed memory values, and returns direct Java argv. The launcher is never executed. `eula.txt` is inspected only for status; GameNode never accepts the Minecraft EULA.

## Launch and expansion

SteamCMD templates declare `platform_launches.windows` and/or `.linux`. `executable` and `working_directory` are relative to the server root, `working_root` is exactly `server_root`, and `arguments` is an array. `environment` is an optional map with uppercase keys. Only these compiled fields expand `{{VARIABLE}}` (legacy `${VARIABLE}` remains supported for normalized Egg data). Unknown or malformed placeholders fail before launch.

Expansion is not recursive, does not read host environment variables, and never evaluates Go/JavaScript expressions. Selected path fields are revalidated after expansion.

## Stop behavior

Stop behavior is `terminate` or a bounded `stdin_command` with a safe single-line command. After the timeout, the normal GameNode lifecycle performs its existing force-kill fallback. Stop scripts, shell commands, and template-defined signals are not supported.

## Variables

Supported types are `string`, `integer`, `number`, `boolean`, `enum`, and `secret`. `secret` must set `sensitive: true`; other types must not. `required` and `nullable` are mutually exclusive. Validation supports numeric `min`/`max`, string `min_length`/`max_length`, enum `allowed`, and a validated `default_value`. There is no expression or regular-expression language.

`name`, `description`, `placeholder`, `group`, and `advanced` are presentation metadata only. The provisioning wizard renders every editable variable from this data, validates it again server-side, places advanced fields after ordinary fields, derives port previews, shows Requirements, and hides sensitive values on review.

## Ports

A port has `name`, `protocol` (`tcp`/`udp`), `required`, an optional `purpose`, and exactly one base source: literal `port` or an editable non-sensitive integer `variable`. A bounded non-negative `offset` may be applied. GameNode resolves and validates every final value, rejects duplicates/conflicts, performs its normal best-effort OS probe, and transactionally registers the rows with the server. It never scans arbitrary game files for ports.

## Expected and configuration files

Each `expected_files` item has a relative `path`, `type` (`file` or `directory`), `required`, optional `platform`, and optional `executable` for files. GameNode checks all required artifacts after installation and before registration, resolves symlinks, and rejects wrong types or root escapes. Optional artifacts are ignored only when absent; if present they remain sandbox-checked.

`config_files` contains only relative `path`, short `format`, and `description`. It is documentation metadata. Actual managed edits require a separately reviewed configuration adapter whose parser and writer are compiled into GameNode.

### Supported configuration adapter formats

Adapter schema v1 covers the three file formats. Adapter schema v2 adds `managed-launch` and keeps every v1 adapter readable and unchanged.

- `xml-properties` updates approved unique XML `<property name="..." value="...">` attributes.
- `ini-key-values` updates approved sectionless `key=value` records.
- `section-tuple-key-values` updates approved typed key/value settings stored in one parenthesized container property inside one configured section, for example `[Server]` followed by `Settings=(Name="Test",Port=1234,Enabled=True)`.
- `managed-launch` stores typed values in GameNode and binds each one to a reviewed launch argument or environment entry. It declares no `target`, `section`, `container_property`, `initialization`, or `post_start_only`.

### Base launch and managed configuration

A template's `platform_launches` entry is the **base launch**: the fixed executable and arguments that never change per server. A `managed-launch` adapter contributes the per-server settings. At start GameNode combines them:

```text
base launch  +  managed configuration  =  runtime launch
```

Nothing is edited by argument index, and the resolved launch is never persisted. A managed setting must have exactly one source of truth: a field key bound by a `managed-launch` adapter may not also appear as a `{{PLACEHOLDER}}` in any base launch executable, argument, working directory, or environment value, and validation rejects a template that does both.

### Field bindings

A schema-v2 field replaces `property` with a `binding`. Binding types are a closed compiled whitelist; there is no expression language, no regular-expression rewriting, and no user-supplied argument name. Argument names and environment names come only from reviewed Official adapter data.

| Binding | Field types | Result |
| --- | --- | --- |
| `launch-value` | any non-secret type | `argument` followed by the value as exactly one argv element |
| `launch-flag` | `boolean` only | `argument` when true, nothing when false |
| `launch-secret` | `secret` only | `argument` followed by the secret, inserted only at process start |
| `environment-value` | any non-secret type | `name=value` in the child environment |
| `environment-secret` | `secret` only | `name=secret` in the child environment |

`launch-value` may declare `true_value`/`false_value` on a boolean field to emit a game-specific token such as `-public 1` or `-public 0`. Both mapped values are single argv values.

Validation rules: an argument matches `-` or `--` followed by letters, digits, and hyphens; an environment name uses the existing uppercase environment key grammar; a secret binding requires a `secret`, `sensitive` field and a non-secret binding forbids one; `launch-flag` and boolean value mapping require a boolean field; two fields may not claim the same argument or environment name; and NUL/newline data is rejected in values and mapped tokens. A user value always stays exactly one argv element and can never add another argument.

`managed-launch` values are persisted per server in `server_config_values` alongside the pinned adapter snapshot. Initial values come from the matching template variables during provisioning, and those keys are deliberately excluded from the server's process environment and template-variable metadata so they are not configured in two places.

`section-tuple-key-values` receives `section` and `container_property` from the reviewed descriptor. It supports quoted and empty strings, integers, numbers, booleans, and enums. Unknown tuple values—including balanced nested values—remain opaque and are preserved; duplicate mapped properties are rejected as ambiguous. Other sections and unrelated file content remain intact. This is a compiled parser for that exact file shape, not a general Unreal Engine parser or a template-defined language.

An adapter may declare `initialization: {"mode":"seed-from-file","source":"..."}`. If the target is absent, GameNode validates both relative paths inside the server root, rejects symlink/reparse escapes, parses and patches the bounded source before creating anything, creates target parents safely, then atomically creates the target. An existing target is never replaced from the seed. Palworld uses this format for `/Script/Pal.PalGameWorldSettings` / `OptionSettings` and seeds `PalWorldSettings.ini` from `DefaultPalWorldSettings.ini` before registration.

## Requirements, compatibility, and provisionability

Requirements use types `os`, `architecture`, `java`, `steamcmd`, `disk`, or `note`, with level `hard` or `informational`. GameNode enforces only facts it can establish safely (currently OS, architecture, and Java availability); disk and notes are hints, not invented enforcement.

Compatibility answers whether GameNode understands the template: `compatible`, `partially_compatible`, or `unsupported`. Provisionability answers whether this node can currently use it. A compatible template can be unavailable because its host launch is missing, Java is absent, an adapter failed validation, or its installer is not managed by the provisioning flow. API/UI summaries use stable safe reasons rather than raw parser, filesystem, or external-tool output.

Stable validation codes include:

- `TEMPLATE_SCHEMA_INVALID`
- `TEMPLATE_UNSUPPORTED_VERSION`
- `TEMPLATE_INVALID_PATH`
- `TEMPLATE_UNSUPPORTED_INSTALLER`
- `TEMPLATE_INVALID_VARIABLE`
- `TEMPLATE_INVALID_PLATFORM_LAUNCH`
- `TEMPLATE_SHELL_SEMANTICS_FORBIDDEN`
- `TEMPLATE_EXPECTED_FILE_INVALID`
- `TEMPLATE_REQUIREMENT_UNAVAILABLE`

## Versioning and server pinning

`id` is the stable identity of a template. `version` is semantic and must be bumped whenever defaults, launch behavior, ports, expected artifacts, adapters, or other behavior changes. Catalog metadata must match the referenced template. At creation, a server stores the template ID, source, and exact version plus its resolved launch, ports, variable sensitivity, and adapter snapshots. A later catalog refresh affects only future creations; GameNode never automatically migrates an existing server to a newer template.

## Complete SteamCMD example

This is a complete parser-valid schema-v2 example. Product facts still require upstream verification before contribution.

```json
{
  "schema_version": 2,
  "id": "example-steam-server",
  "name": "Example Steam Server",
  "description": "Contributor example for a direct native dedicated server.",
  "version": "1.0.0",
  "category": "steamcmd",
  "tags": ["example", "steamcmd"],
  "source_type": "official",
  "source_identifier": "example-steam-server",
  "source_format_version": "2",
  "source_metadata": {"author": "GameNode", "tags": ["example", "steamcmd"]},
  "installer": {"type": "steamcmd", "steamcmd": {"app_id": 294420, "validate": true, "login_mode": "anonymous", "platform": "native", "install_target": "server_root"}},
  "platform_launches": {
    "windows": {"executable": "DedicatedServer.exe", "arguments": ["--port", "{{SERVER_PORT}}"], "working_root": "server_root", "stop_method": "terminate", "stop_timeout_seconds": 30}
  },
  "variables": [
    {"name": "Server port", "description": "Primary game port.", "key": "SERVER_PORT", "default_value": "27015", "user_viewable": true, "user_editable": true, "type": "integer", "sensitive": false, "required": true, "nullable": false, "validation": {"min": 1024, "max": 65535}, "group": "Network"}
  ],
  "ports": [{"name": "Game", "protocol": "udp", "variable": "SERVER_PORT", "required": true, "purpose": "Primary game traffic"}],
  "expected_files": [{"path": "DedicatedServer.exe", "type": "file", "required": true, "executable": true, "platform": "windows"}],
  "requirements": [{"type": "steamcmd", "level": "hard", "value": "anonymous", "description": "Managed anonymous SteamCMD installation is required."}],
  "help": {"summary": "GameNode installs once and launches the native executable directly."},
  "compatibility": {"status": "compatible", "findings": []},
  "read_only": true,
  "platforms": ["windows"]
}
```

The production references cover several different shapes: 7 Days to Die is a Windows/Linux SteamCMD direct binary with an XML adapter; Project Zomboid is Windows SteamCMD with a reviewed direct bundled-Java invocation replacing an upstream batch wrapper and a post-start INI adapter; Minecraft NeoForge is an existing-files Java/argfile resolver; Palworld is a Windows SteamCMD direct binary with a seeded section/tuple adapter; Satisfactory is a Windows SteamCMD direct binary whose verified settings are expressed entirely through structured launch arguments and ports; Valheim is a Windows SteamCMD direct binary whose settings are a schema-v2 `managed-launch` adapter, because the game reads them only from process arguments.

## Validation and contribution workflow

Copy the nearest production `template.json`, choose a unique stable ID, define one whitelisted installer, add an explicit launch for every claimed platform, define typed variables/ports/artifacts/requirements, and update `catalog.json` in the same change. `go test ./internal/templates` parses the manifest, every referenced template, and every referenced adapter; it asserts unique IDs, metadata agreement, supported schema/installer/platforms, safe paths, valid launches/variables/ports, and Golden expectations for all reference architectures. Normal `go test ./...` makes this a CI gate.

Before submitting:

- Confirm the unique ID and bump the semantic template version.
- Add/update the catalog entry and matching schema version/tags/platforms/installer.
- Confirm every advertised platform, executable, working directory, argument, port, and stop behavior against upstream documentation or a real installation.
- Use no shell, scripts, absolute paths, traversal, URLs, credentials, or container fields.
- Type every variable; mark only secrets sensitive and never interpolate them into paths/args.
- Declare and locally verify all required artifacts.
- Run backend/frontend validation and record any real provision/start/console/stop test accurately; do not claim an unexecuted smoke test.

Automatic updates, arbitrary downloads, credentialed Steam login, Workshop, general config editors, shell hooks, container templates, and plugin languages are separate future milestones.
