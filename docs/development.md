# Development

## Prerequisites

- Go 1.23 or newer
- Node.js 22 or newer and npm

## Run in development

Copy `config.example.yaml` to `config.yaml`. Start the backend with `go run ./cmd/gamenode -config config.yaml`. In another shell, run `npm ci` and `npm run dev` in `web`. Vite proxies `/api` to the Go server.

## Verification

Run `go test ./...`, `go test -race ./...`, and `go vet ./...`, then `npm ci`, `npm run check`, `npm run test:helpers`, and `npm run build` in `web`. The race detector must run on the same operating system and architecture as the produced test binary; CI runs it on native Linux amd64. See `docs/ci.md` for the complete CI job and artifact matrix.

## GitHub Actions CI and releases

`.github/workflows/ci.yml` runs for pull requests to `master`, pushes to `master`, and manual dispatches. Linux CI checks formatting, vet, tests, builds, and `go test -race ./...`; a native Windows runner executes the Go suite and build, including Windows-specific filesystem and runtime tests. The frontend job uses Node.js 22 and runs `npm ci`, `npm run check`, `npm run test:helpers`, and `npm run build`. Packaging jobs rebuild the production frontend immediately before compiling each binary, so the Go `embed` package receives current assets.

Successful pushes to `master` upload unsigned development artifacts named `gamenode-windows-amd64` and `gamenode-linux-amd64`; they are retained for 14 days and are not GitHub Releases.

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

The representative 7 Days to Die fixture is `internal/templates/testdata/7-days-to-die.json`. It mirrors the upstream Pelican Egg structures relevant to GameNode while avoiding a full third-party snapshot. Parser, startup, expansion, SteamCMD detection, limits, secret handling, and persistence tests live in `internal/templates`; endpoint/RBAC/CSRF/audit tests live in `internal/api/templates_test.go`; pure UI helpers are covered by `web/tests/templates-helpers.test.ts`.

The normal test suite performs no network access, launches no shell, starts no Docker runtime, and downloads no SteamCMD archive. When extending compatibility, add stable finding codes and tests before changing status behavior. Do not broaden the startup parser into shell emulation.

# SteamCMD provisioning development

SteamCMD unit tests use mocked downloaders/runners and in-memory archives. Provisioning tests cover success, validation, cancellation, target conflicts, concurrency, interrupted jobs, installation/database failure, platform gating, and absence of ghost servers. API tests cover authentication, independent RBAC, CSRF, ownership, audit, sanitized errors, and secret redaction.

Run race-sensitive packages explicitly:

```sh
go test -race ./internal/steamcmd ./internal/provisioning
```

An opt-in official bootstrap smoke test is available and is skipped by default:

```sh
GAMENODE_STEAMCMD_INTEGRATION=1 go test ./internal/steamcmd -run TestManagedBootstrapIntegration
```

Manual 7 Days to Die acceptance (large download): import `internal/templates/testdata/7-days-to-die.json`, open Templates, select Create server, choose a new directory name, configure variables, and start provisioning. Confirm App ID `294420`, completion to a normal server record, safe expanded executable/arguments, Files/Console/Monitoring behavior, and Stop. The representative imported launch is Linux-specific; perform start/runtime acceptance on Linux. On Windows, provisionability must reject it unless the template contains an independently safe Windows launch—do not substitute a guessed executable. Do not enable automatic update-on-start when testing `AUTO_UPDATE`.
