# Development

## Prerequisites

- Go 1.23 or newer
- Node.js 22 or newer and npm

## Run in development

Copy `config.example.yaml` to `config.yaml`. Start the backend with `go run ./cmd/gamenode -config config.yaml`. In another shell, run `npm ci` and `npm run dev` in `web`. Vite proxies `/api` to the Go server.

On Windows, `./start-dev.ps1` starts both development processes in separate terminals. It uses `npm.cmd`, so restrictive PowerShell execution policies do not block the Node commands, and starts terminals without a PowerShell profile. It uses `config.yaml` beside the script by default; pass `-Config C:\path\to\config.yaml` to select another configuration file. The helper passes the explicit backend flag `-dev`, which creates or refreshes the local administrator `dev` with password `dev` on every development start. These credentials are intentionally weak and are never enabled by normal or release startup.

The same behavior can be used manually with `go run ./cmd/gamenode -config config.yaml -dev`. Do not use `-dev` for a shared, reachable, staging, or production instance.

## Verification

Run `go test ./...`, `go test -race ./...`, and `go vet ./...`, then `npm ci`, `npm run check`, `npm run test:helpers`, and `npm run build` in `web`. The race detector must run on the same operating system and architecture as the produced test binary; CI runs it on native Linux amd64. See `docs/ci.md` for the complete CI job and artifact matrix.

Tenant/RBAC-scope isolation has its own regression suite, `internal/api/cross_tenant_test.go`, run as part of `go test ./internal/api`; it builds two fully independent tenants and asserts uniform `403` cross-tenant denial plus dashboard/list non-leakage. Pure tenant frontend logic (slug derivation, name/slug validation, membership candidate filtering, tenant selector locking) is covered by `web/tests/tenants-helpers.test.ts`, part of `npm run test:helpers`.

Remote Node Foundation (v0.5A) backend coverage lives in `internal/nodeidentity`, `internal/nodes`, `internal/remote` (own package test suites, including `httptest`-backed transport/redirect/timeout/oversized-response cases for the remote client), and `internal/api/node_test.go` (pairing/enrollment end-to-end, machine-auth rejection of a browser session, RBAC/CSRF on the controller-facing registry API). Frontend health/compatibility/capability formatting and endpoint/pairing-token validation are covered by `web/tests/nodes-helpers.test.ts`.

Theme resolution and wallpaper/preference sanitization (`web/src/theme.ts`) are covered by `web/tests/theme.test.ts`; the shared server/health status-to-tone mapping (`web/src/server-status.ts`) is covered by `web/tests/server-status.test.ts`. Both run as part of `npm run test:helpers`. The wallpaper file-processing path (`web/src/wallpaper.ts`) needs a real `createImageBitmap`/canvas and is verified manually in a browser instead (see the UI theme section of `docs/architecture.md`).

## GitHub Actions CI and releases

`.github/workflows/ci.yml` runs for pull requests to `main`, pushes to `main`, and manual dispatches. Linux CI checks formatting, vet, tests, builds, and `go test -race ./...`; a native Windows runner executes the Go suite and build, including Windows-specific filesystem and runtime tests. The frontend job uses Node.js 22 and runs `npm ci`, `npm run check`, `npm run test:helpers`, and `npm run build`. Packaging jobs rebuild the production frontend immediately before compiling each binary, so the Go `embed` package receives current assets.

Successful pushes to `main` upload unsigned development artifacts named `gamenode-windows-amd64` and `gamenode-linux-amd64`; they are retained for 14 days and are not GitHub Releases.

Push a semantic version tag (for example `git tag v0.1.0` followed by `git push origin v0.1.0`) to start `.github/workflows/release.yml`. It repeats Linux and Windows verification, produces `gamenode-windows-amd64.exe` and `gamenode-linux-amd64`, writes `SHA256SUMS.txt`, then creates a draft GitHub Release with automatic notes and publishes it only after all assets were uploaded. The release binaries receive the tag, commit SHA, and UTC build time through Go linker flags; Diagnostics exposes these values without requiring runtime configuration.

### Constrained local environments

Endpoint protection or a managed development environment can block Go test executables, Go module-cache reads, or local npm command shims. Treat those as environment blockers, not product failures: do not weaken application security or alter build scripts to work around them. Record the exact blocked command separately, continue with independent static/build checks where possible, and complete runtime/browser acceptance on an unrestricted supported machine.

### Windows acceptance record (2026-08-11)

The native Windows pass completed `npm ci`, frontend check/helper tests/production build, Go vet/build/full tests, the targeted critical-package tests, Windows release build, Linux amd64 cross-build, and the root-level Windows lifecycle/console/RBAC/filesystem harnesses. The fresh harness markers were `E2E_WEBSOCKET_OK`, `E2E_MILESTONE3_OK`, `E2E_RBAC_MILESTONE5B_OK`, and `E2E_FILESYSTEM_MILESTONE4_OK`; the Windows junction test passed. The account could not create a symbolic link (`A required privilege is not held by the client`), so that one OS-level check is an explicit platform skip. `go test -race` could not run locally because `CGO_ENABLED=0` and `gcc` is unavailable. Linux runtime smoke testing requires a native Linux/WSL environment; the cross-build itself passed.

## Cross-platform artifacts

Run `npm run build` first. In PowerShell, build the artifacts with:

```powershell
New-Item -ItemType Directory -Force dist | Out-Null
$env:GOOS='windows'; $env:GOARCH='amd64'; go build -o dist/gamenode-windows-amd64.exe ./cmd/gamenode
$env:GOOS='linux'; $env:GOARCH='amd64'; go build -o dist/gamenode-linux-amd64 ./cmd/gamenode
Remove-Item Env:GOOS, Env:GOARCH
```

The executable defaults to `127.0.0.1:8443` and `./data/gamenode.db`; pass `-config path/to/config.yaml` to override it. Production TLS is enabled by setting both `server.tls_cert` and `server.tls_key`.

## Native runtime verification

The runtime tests use a small platform-native test process rather than a real game server. On Windows, the test suite starts and controls `ping.exe`; Linux runtime code is compiled into the Linux artifact. Review `docs/runtime.md` before deploying wrapper scripts or long-running production servers.

## Support bundle Windows acceptance smoke

On an unrestricted Windows machine, log in as an administrator, open Settings, and generate a support bundle. Confirm the browser downloads a safely named ZIP, the archive parses, and it contains exactly `manifest.json`, `diagnostics.json`, `settings.json`, `audit-recent.json`, and `servers.json`; each entry must be valid JSON. Confirm a Settings.View-only user cannot generate, a global Settings.Manage user can, and a server-scoped Settings.Manage user is denied. Confirm CSRF is required, one successful request creates one `support.bundle_generate` success event, and a controlled generation failure creates one sanitized failure event with no raw error in either the response or audit record. Inspect that the archive excludes secrets, host paths, server content, logs, and console data. The generation event itself need not appear in the bundle that triggered it. Record `E2E_SUPPORT_MILESTONE7D_OK` only after these steps pass on that machine.

## Service deployment

v0.1 produces portable Windows and Linux binaries only. Install the binary under a dedicated, least-privileged service account and provide an absolute configuration path. A service installer/unit is deliberately not bundled.
# Template import development

## Official Game Library development

Official template source files live under `templates/`, with `templates/catalog.json` as the only discovery manifest. Add a category JSON file, add its matching catalog entry, increment the template version for behavioral changes, and run the backend plus frontend helper suites before review. IDs remain stable. Both files must merge to `main` before the production fixed GitHub Raw source sees them; short Raw cache propagation delays are expected. There is deliberately no UI/config override for arbitrary sources.

Catalog tests use local stubs and TLS test servers and perform no internet access. They cover schema/JSON/size/status/timeout/redirect/path rules, partial invalid templates, minimum GameNode compatibility, initial and replacement cache writes, offline fallback, corrupted cache, and preservation of last-good state. Existing servers are configuration snapshots and are never migrated when a template version changes.

The representative 7 Days to Die fixture is `internal/templates/testdata/7-days-to-die.json`. It mirrors the upstream Pelican Egg structures relevant to GameNode while avoiding a full third-party snapshot. Parser, startup, expansion, SteamCMD detection, limits, secret handling, and persistence tests live in `internal/templates`; endpoint/RBAC/CSRF/audit tests live in `internal/api/templates_test.go`; pure UI helpers are covered by `web/tests/templates-helpers.test.ts`.

The normal test suite performs no network access, launches no shell, starts no Docker runtime, and downloads no SteamCMD archive. When extending compatibility, add stable finding codes and tests before changing status behavior. Do not broaden the startup parser into shell emulation.

# Minecraft NeoForge reference development

`server-test` is an optional local real-world reference and is intentionally not shipped as product data. The current fixture contains generated Windows/Linux launchers, `user_jvm_args.txt`, NeoForge `26.2.0.59` argfiles, and Minecraft `26.2` libraries. `internal/templates/neoforge_test.go` exercises both platform shapes, the local reference when present, missing/malformed files, extra commands, shell operators, absolute/traversal paths, and typed memory assembly. The Windows console smoke test exercises stdout, stderr, stdin, restart/session replacement, and graceful stdin-command stop through the native runtime.

For full acceptance, install a compatible Java runtime, expose it through `JAVA_HOME` or `PATH`, open Templates → Minecraft NeoForge → Create server, select the existing directory, inspect the preview, and adopt. Start the server, observe both console streams, send `help`, connect/reconnect two console clients, then Stop and Restart. Do not change `eula.txt`; if the server exits for an unaccepted EULA, acceptance pauses for the user to review and accept it outside GameNode.

# SteamCMD provisioning development

SteamCMD unit tests use mocked downloaders/runners and in-memory archives. Provisioning tests cover success, validation, cancellation, target conflicts, concurrency, interrupted jobs, installation/database failure, platform gating, and absence of ghost servers. API tests cover authentication, independent RBAC, CSRF, ownership, audit, sanitized errors, and secret redaction.

## Adding an Official SteamCMD game

1. Verify the dedicated-server App ID and anonymous-login support from a maintained source.
2. Verify supported host platforms independently.
3. Verify the exact relative executable for each declared platform; never infer one platform from another.
4. Define a structured argument array, with placeholders only for declared typed variables.
5. Use `terminate` unless a bounded, reliable console stop command is documented.
6. Declare only known ports and connect editable ports to validated integer variables.
7. Add the schema-v1 JSON under `templates/steamcmd/` and run the repository catalog validation tests.
8. Add the matching `catalog.json` entry and start its template version at `1.0.0`; bump it for behavioral changes.
9. Run Go, UI helper, build, and cross-build checks.
10. Optionally run a real provision/start/console/stop smoke outside CI and record the host, upstream build, and result. Large games are never downloaded by unit tests.

## Adding a configuration adapter

1. Keep `template.json`, adapter JSON, README, and fixtures in one game directory.
2. Add a same-directory adapter reference to the template; URLs and nested paths are forbidden.
3. Select only a format implemented in `internal/gameconfig` (`xml-properties`, flat `ini-key-values`, or `section-tuple-key-values` in schema v1).
4. Use a safe relative target and simple property identifiers—never XPath, regex, scripts, hooks, or executable configuration syntax.
5. Map initial fields to existing template variables with matching semantics. Configuration-only fields are allowed only on `post_start_only` INI adapters whose authoritative file is generated by the game.
6. Increment adapter version for mapping behavior and template version for product behavior.
7. Add a minimal sanitized fixture and parser/writer tests covering real upstream shape, escaping, duplicates, missing properties, size/depth limits, and traversal.
8. Test initial provisioning, persisted snapshot, offline catalog cache, API RBAC/CSRF, secret redaction, backup, and atomic replacement.
9. Verify existing servers continue using their stored snapshot after a remote adapter update.
10. Document restart requirements and perform an opt-in real-game acceptance when practical.

Run race-sensitive packages explicitly:

```sh
go test -race ./internal/steamcmd ./internal/provisioning ./internal/servers ./internal/serverupdates
```

## Manual SteamCMD server update development (v0.2.1)

`internal/serverupdates` tests reuse the `internal/provisioning` conventions (real SQLite via `internal/database`, hand-written fake `Installer` with cancellation channels) but need no `TemplateSource`/ports/config-adapter fixtures. Cover at minimum: successful update, validate on/off, the trusted App ID/plan reaching the installer unchanged, cancellation mid-run, SteamCMD failure, post-update missing-executable failure, running/starting/stopping-server rejection, concurrent same-server rejection, independent-server concurrency, interrupted-job recovery, and an ineligible (no persisted metadata) server. `internal/servers` additionally covers the `BeginUpdate`/release reservation and its Start/Restart/Delete guards, plus `VerifyLaunchExecutablePresent`'s sandbox checks. `internal/api` tests mirror `provisioning_test.go`'s auth/RBAC/CSRF/ownership/audit shape for `/servers/{id}/update` and `/server-update-jobs/{id}`.

When adding or reviewing this feature, verify a template version bump on the Official catalog never changes an existing server's update behavior: a server's `server_steamcmd_provisioning` snapshot is immutable once written, so an update always re-installs the App ID pinned at provisioning time regardless of the current catalog.

An opt-in official bootstrap smoke test is available and is skipped by default:

```sh
GAMENODE_STEAMCMD_INTEGRATION=1 go test ./internal/steamcmd -run TestManagedBootstrapIntegration
```

Manual 7 Days to Die acceptance (large download): import `internal/templates/testdata/7-days-to-die.json`, open Templates, select Create server, choose a new directory name, configure variables, and start provisioning. Confirm App ID `294420`, completion to a normal server record, safe expanded executable/arguments, Files/Console/Monitoring behavior, and Stop. The representative imported launch is Linux-specific; perform start/runtime acceptance on Linux. On Windows, provisionability must reject it unless the template contains an independently safe Windows launch—do not substitute a guessed executable. Do not enable automatic update-on-start when testing `AUTO_UPDATE`.

Project Zomboid has two explicit Windows opt-in tests. Both require an isolated path with sufficient free space and are skipped by normal CI:

```powershell
$env:GAMENODE_PZ_ACCEPTANCE_DATA='C:\temp\gamenode-pz-download'
go test ./internal/steamcmd -run '^TestProjectZomboidInstallIntegration$' -count=1 -v

$env:GAMENODE_PZ_FULL_ACCEPTANCE_DATA='C:\temp\gamenode-pz-full'
go test ./internal/provisioning -run '^TestProjectZomboidFullDeploymentIntegration$' -count=1 -v
```

The full test loads the repository catalog, provisions App ID `380870` through the production SteamCMD/service path, verifies the installed bundled Java executable, current Official provenance and UDP ports, starts the normal GameNode server, handles the first-boot administrator-password prompt without logging the generated secret, waits for `SERVER STARTED`, sends `quit` through the attached console, and requires a stopped state. It can reuse only its own validated completed acceptance directory for runtime troubleshooting. Template `2.0.0` exposes selected values from generated `Server/gamenode.ini` after first start through the compiled INI adapter; `SandboxVars.lua` remains deliberately unmanaged.

Palworld has a separate opt-in Windows acceptance and always requires a new empty isolated directory:

```powershell
$env:GAMENODE_PALWORLD_FULL_ACCEPTANCE_DATA='C:\temp\gamenode-palworld-full'
go test ./internal/provisioning -run '^TestPalworldFullDeploymentIntegration$' -count=1 -v -timeout 75m
```

It provisions App ID `2394010`, validates and seeds the real downloaded defaults, proves all mapped values plus unknown-property preservation, registers the three reserved ports, exercises a managed config edit with secret masking, starts/stops/restarts `PalServer.exe`, and retains the configuration. The stop observation remains conservative: native Windows terminate success is not proof of an application-level graceful save.

Satisfactory has a Windows-only opt-in acceptance. Use a new empty directory with sufficient storage:

```powershell
$env:GAMENODE_SATISFACTORY_FULL_ACCEPTANCE_DATA='C:\temp\gamenode-satisfactory-full'
go test ./internal/provisioning -run '^TestSatisfactoryFullDeploymentIntegration$' -count=1 -v -timeout 75m
```

The acceptance provisions App ID `1690800`, verifies `FactoryServer.exe`, structured port/player arguments, three registered port rows, start stability, Windows `console_interrupt` stop behavior, and restart. It does not claim or configure the server through the game API.
# Container development

Normal unit tests use a fake Engine and fake Container installer and do not require
Docker. Container Egg tests cover image/startup/config analysis, bounded installer
specification, fake-engine provisioning, snapshot persistence, cancellation and
cleanup. Real acceptance is opt-in and requires a Linux-accessible Docker Engine
socket; it should use a small public image and explicitly Pull before Start. Docker
CLI is not part of the product test path. Real Egg acceptance must use a disposable
target and must verify that the host process never receives the Egg shell command.
