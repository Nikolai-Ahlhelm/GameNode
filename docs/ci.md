# Continuous integration and release artifacts

GitHub Actions runs `.github/workflows/ci.yml` for pushes, pull requests, and manual dispatches. The workflow is limited to verification and packaging; it does not deploy or publish a release.

## Required checks

- `Go fmt, vet and test` verifies that all Go files are formatted, then runs `go vet ./...` and `go test ./...`.
- `Go race detector (Linux amd64)` runs `CGO_ENABLED=1 go test -race ./...` on a native Linux amd64 runner. This is intentionally not a cross-compiled job: the race detector needs a runnable binary and a C toolchain, both provided by the Ubuntu GitHub-hosted runner.
- `Frontend check and build` runs `npm ci`, `npm run check`, and `npm run build` with Node.js 22.

## Build artifacts

After the frontend check succeeds, the workflow builds the frontend again in each packaging job so the Go binary embeds the exact generated web assets. It uploads these downloadable workflow artifacts:

| Job | Runner | Artifact | Contents |
| --- | --- | --- | --- |
| `Windows amd64 build` | `windows-latest` | `gamenode-windows-amd64` | `gamenode-windows-amd64.exe` |
| `Linux amd64 build` | `ubuntu-latest` | `gamenode-linux-amd64` | `gamenode-linux-amd64` |

Artifacts are CI build outputs, not signed release packages. Before a public release, download the artifacts from the successful workflow run, apply the project's chosen signing and checksum process, and attach the resulting files to the release.

## Temporary E2E helpers

The root-level `tmp-e2e-helper.go`, `tmp-e2e-client.go`, and related `tmp-e2e-*` files are manual Windows smoke-test helpers. They compile and launch a locally built binary, then exercise setup, server lifecycle, and WebSocket console behavior against fixed paths and a fixed port. They are deliberately excluded from normal Go package discovery with `//go:build ignore`.

When this coverage is formalized, move the helper process into a test-only package such as `internal/testutil` and place the scenario in an `e2e/` Go test package. Use `t.TempDir()` for data and configuration, reserve a loopback port dynamically, start the server through a test harness, and use context-bound cleanup. Keep Windows console scenarios behind a `windows` build tag and add a Linux-compatible scenario where applicable. A dedicated, separately triggered E2E workflow job can then run the suite without changing production APIs, runtime code, or data models.
