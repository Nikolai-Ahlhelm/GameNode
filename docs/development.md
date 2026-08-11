# Development

## Prerequisites

- Go 1.23 or newer
- Node.js 22 or newer and npm

## Run in development

Copy `config.example.yaml` to `config.yaml`. Start the backend with `go run ./cmd/gamenode -config config.yaml`. In another shell, run `npm install` and `npm run dev` in `web`. Vite proxies `/api` to the Go server.

## Verification

Run `go test ./...`, `go test -race ./...`, and `go vet ./...`, then `npm run check` and `npm run build` in `web`. The race detector must run on the same operating system and architecture as the produced test binary; CI runs it on native Linux amd64. See `docs/ci.md` for the complete CI job and artifact matrix.

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

## Service deployment

Milestone 1 produces portable Windows and Linux binaries only. Install the binary under a dedicated, least-privileged service account and provide an absolute configuration path. A service installer/unit is deliberately not bundled yet; this avoids making platform-specific installer decisions before the runtime milestone.
