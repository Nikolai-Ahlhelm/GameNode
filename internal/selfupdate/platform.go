package selfupdate

// ChecksumAsset is the fixed name of the checksum manifest every release
// publishes (see .github/workflows/release.yml's "Create checksums" step).
const ChecksumAsset = "SHA256SUMS.txt"

// AssetName returns the release asset that carries the GameNode binary for a
// platform. The mapping is fixed in code and mirrors the release workflow's
// published artifacts; no asset name is ever taken from the release API
// response, a template, or a caller.
func AssetName(goos, goarch string) (string, bool) {
	switch {
	case goos == "windows" && goarch == "amd64":
		return "gamenode-windows-amd64.exe", true
	case goos == "linux" && goarch == "amd64":
		return "gamenode-linux-amd64", true
	}
	return "", false
}
