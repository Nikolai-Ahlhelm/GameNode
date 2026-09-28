package selfupdate

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrChecksumMissing = errors.New("checksum manifest has no entry for the release asset")
var ErrChecksumMalformed = errors.New("checksum manifest is malformed")

// ParseChecksum extracts the SHA-256 digest recorded for one asset from a
// `sha256sum`-format manifest ("<64 hex>  <name>" or "<64 hex> *<name>").
// Conflicting duplicate entries are rejected rather than resolved by order.
func ParseChecksum(manifest []byte, asset string) ([32]byte, error) {
	var found [32]byte
	have := false
	scanner := bufio.NewScanner(bytes.NewReader(manifest))
	scanner.Buffer(make([]byte, 0, 4096), 4096)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 66 {
			return found, ErrChecksumMalformed
		}
		digest, rest := line[:64], line[64:]
		if rest[0] != ' ' {
			return found, ErrChecksumMalformed
		}
		name := strings.TrimPrefix(strings.TrimPrefix(rest[1:], "*"), " ")
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != 32 {
			return found, ErrChecksumMalformed
		}
		if name != asset {
			continue
		}
		var candidate [32]byte
		copy(candidate[:], raw)
		if have && candidate != found {
			return found, ErrChecksumMalformed
		}
		found, have = candidate, true
	}
	if err := scanner.Err(); err != nil {
		return found, ErrChecksumMalformed
	}
	if !have {
		return found, ErrChecksumMissing
	}
	return found, nil
}
