package selfupdate

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const minBinaryBytes = 1 << 20

var errNotExecutable = errors.New("not a valid executable for this platform")

// validateExecutable checks the container format and CPU architecture of a
// staged binary so an asset built for another platform is rejected before it
// is ever executed or installed. It is a plausibility check layered on the
// checksum, not a substitute for it.
func validateExecutable(path, goos string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < minBinaryBytes {
		return errNotExecutable
	}
	file, err := os.Open(path)
	if err != nil {
		return errNotExecutable
	}
	defer file.Close()
	header := make([]byte, 64)
	if _, err := io.ReadFull(file, header); err != nil {
		return errNotExecutable
	}
	switch goos {
	case "linux":
		// ELF, 64-bit, little-endian, machine x86-64.
		if !bytes.HasPrefix(header, []byte{0x7f, 'E', 'L', 'F'}) || header[4] != 2 || header[5] != 1 || binary.LittleEndian.Uint16(header[18:20]) != 0x3E {
			return errNotExecutable
		}
	case "windows":
		if header[0] != 'M' || header[1] != 'Z' {
			return errNotExecutable
		}
		offset := int64(binary.LittleEndian.Uint32(header[0x3C:0x40]))
		pe := make([]byte, 6)
		if _, err := file.ReadAt(pe, offset); err != nil {
			return errNotExecutable
		}
		// "PE\0\0" followed by machine AMD64.
		if !bytes.HasPrefix(pe, []byte{'P', 'E', 0, 0}) || binary.LittleEndian.Uint16(pe[4:6]) != 0x8664 {
			return errNotExecutable
		}
	default:
		return errNotExecutable
	}
	return nil
}

// runVersionSelfTest executes the staged binary with the fixed "--version"
// argument (structured exec, no shell) and returns the version it reports. It
// proves the binary loads and runs on this host before it replaces the
// current one. The binary has already matched the release checksum.
func runVersionSelfTest(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out limitedBuffer
	var err error
	// A freshly written executable can briefly report ETXTBSY ("text file
	// busy") on Linux if another goroutine forked while its write descriptor
	// was still open. That resolves within moments, so retry a few times.
	for attempt := 0; attempt < 5; attempt++ {
		out = limitedBuffer{limit: 512}
		cmd := exec.CommandContext(ctx, path, "--version")
		cmd.Stdout = &out
		if err = cmd.Run(); err == nil || !strings.Contains(err.Error(), "text file busy") {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(out.buf.String()))
	if len(fields) != 2 || fields[0] != "gamenode" {
		return "", errors.New("unexpected version output")
	}
	return fields[1], nil
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.limit - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}
