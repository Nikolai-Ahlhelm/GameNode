# GameNode

GameNode is a self-contained local game-server management platform. This repository currently implements **Milestone 2 — Server Runtime**: setup, local authentication, an embedded web UI, configuration, SQLite migrations, native-server CRUD, and lifecycle control for native applications.

## Quick start

1. Install Go 1.23+ and Node.js 22+.
2. Copy `config.example.yaml` to `config.yaml`.
3. For development, run `go run ./cmd/gamenode -config config.yaml` and, in `web`, `npm install; npm run dev`.
4. Open the Vite URL and create the first administrator.

For a production-style single binary, run `npm run build` in `web`, then `go build ./cmd/gamenode`; open `http://127.0.0.1:8443`.

The initial administrator is created exactly once. After setup, only the login route is available. For production, configure TLS so the authentication cookie is marked `Secure`.

Use **Servers** to register a native application. `Adopt Existing` registers paths only and never modifies, moves, or installs files. GameNode launches the configured executable directly with the supplied argument list; it does not run arbitrary shell command strings. See `docs/runtime.md` for lifecycle and platform limitations.

## Tests and builds

```powershell
go test ./...
go test -race ./...
go vet ./...
Push-Location web; npm install; npm run check; npm run build; Pop-Location
$env:GOOS='windows'; $env:GOARCH='amd64'; go build -o dist/gamenode-windows-amd64.exe ./cmd/gamenode
$env:GOOS='linux'; $env:GOARCH='amd64'; go build -o dist/gamenode-linux-amd64 ./cmd/gamenode
```

The binary embeds the built frontend and does not require Node.js at runtime. See `docs/development.md` for local verification and `docs/ci.md` for CI checks and downloadable build artifacts.
