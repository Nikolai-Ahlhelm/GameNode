# Continuous integration and releases

GameNode uses two deliberately separate GitHub Actions workflows.

## CI for `main`

`.github/workflows/ci.yml` runs on pull requests targeting `main`, pushes to `main`, and manual dispatches. It uses Go 1.23 and Node.js 22 from GitHub-hosted runners; all frontend commands use the repository lockfile and local npm dependencies.

| Job | Runner | Checks |
| --- | --- | --- |
| `Backend (Linux)` | Ubuntu | `go mod download`, tracked-file `gofmt` check, `go vet ./...`, `go test ./...`, `go build ./...` |
| `Backend (Windows)` | Windows | Native `go test ./...` and `go build ./...`, including platform-specific runtime and filesystem coverage |
| `Go race detector (Linux amd64)` | Ubuntu | `CGO_ENABLED=1 go test -race ./...` |
| `Frontend` | Ubuntu | `npm ci`, `npm run check`, `npm run test:helpers`, `npm run build` |
| Windows/Linux package jobs | Native target runner | Fresh `npm ci` and production frontend build before the final embedded binary build |

The Go and npm download caches improve run time but are never required for correctness. Native Windows tests retain their normal privilege-dependent skips; the workflow does not disable symlink or reparse-point coverage to force a green result.

On successful pushes to `main`, the package jobs upload unsigned development artifacts for 14 days:

| Artifact | File |
| --- | --- |
| `gamenode-windows-amd64` | `gamenode-windows-amd64.exe` |
| `gamenode-linux-amd64` | `gamenode-linux-amd64` |

These artifacts are CI outputs, not official releases.

## Versioned releases

`.github/workflows/release.yml` runs only when a pushed tag matches the workflow tag prefix `v*`; its verification job rejects tags that are not semantic versions such as `v0.1.0`, `v0.1.1`, or `v0.2.0` (pre-release and build metadata are also accepted).

The workflow repeats the Linux Go/frontend verification, the Windows Go suite, and the Linux race pass. Only then do native Windows and Linux packaging jobs run. Each packaging job performs `npm ci` and `npm run build` before `go build`, ensuring the executable's `go:embed` frontend is freshly generated. The publish job downloads both binaries, creates `SHA256SUMS.txt` with names matching the assets, creates a draft release with GitHub-generated notes, and publishes it only after the asset upload succeeds.

The resulting GitHub Release contains:

- `gamenode-windows-amd64.exe`
- `gamenode-linux-amd64`
- `SHA256SUMS.txt`

Release builds inject the tag, commit SHA, and UTC build time into the safe Diagnostics application metadata. The release workflow has `contents: write` only for this purpose; normal CI has read-only repository contents permission. No repository secrets are needed: publishing uses the workflow `GITHUB_TOKEN`.

To release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

After downloading a release asset, verify it against `SHA256SUMS.txt` with the platform's SHA-256 tool before deployment.
# Container runtime checks

CI validates the Engine boundary with deterministic fakes. It does not claim a
real Docker daemon acceptance test; that remains an opt-in environment check.
