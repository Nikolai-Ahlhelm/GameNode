package selfupdate

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseChecksum(t *testing.T) {
	a := strings.Repeat("ab", 32)
	b := strings.Repeat("cd", 32)
	manifest := []byte(a + "  gamenode-windows-amd64.exe\n" + b + " *gamenode-linux-amd64\r\n\n")
	got, err := ParseChecksum(manifest, "gamenode-linux-amd64")
	if err != nil || hex.EncodeToString(got[:]) != b {
		t.Fatalf("binary-mode entry: %x %v", got, err)
	}
	got, err = ParseChecksum(manifest, "gamenode-windows-amd64.exe")
	if err != nil || hex.EncodeToString(got[:]) != a {
		t.Fatalf("text-mode entry: %x %v", got, err)
	}
	if _, err = ParseChecksum(manifest, "other"); err != ErrChecksumMissing {
		t.Fatalf("missing entry: %v", err)
	}
}

func TestParseChecksumRejectsMalformedAndConflicting(t *testing.T) {
	good := strings.Repeat("ab", 32)
	for name, manifest := range map[string]string{
		"short digest":  "abcd  gamenode-linux-amd64\n",
		"non hex":       strings.Repeat("zz", 32) + "  gamenode-linux-amd64\n",
		"no separator":  good + "gamenode-linux-amd64\n",
		"conflicting":   good + "  gamenode-linux-amd64\n" + strings.Repeat("cd", 32) + "  gamenode-linux-amd64\n",
		"garbage line":  "hello world, not a checksum manifest at all, sorry about that mate and friends\n",
		"oversize line": strings.Repeat("a", 5000) + "\n",
	} {
		if _, err := ParseChecksum([]byte(manifest), "gamenode-linux-amd64"); err == nil || err == ErrChecksumMissing {
			t.Errorf("%s: expected malformed error, got %v", name, err)
		}
	}
	// A repeated identical entry is harmless.
	if _, err := ParseChecksum([]byte(good+"  x\n"+good+"  x\n"), "x"); err != nil {
		t.Errorf("identical duplicate: %v", err)
	}
}
