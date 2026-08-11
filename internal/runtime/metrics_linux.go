//go:build linux

package runtime

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (nativeRuntime) Metrics(_ context.Context, identity Identity) (Metrics, error) {
	if err := verifyLinux(identity); err != nil {
		return Metrics{}, err
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(identity.PID), "stat"))
	if err != nil {
		return Metrics{}, err
	}
	end := strings.LastIndex(string(stat), ")")
	if end < 0 {
		return Metrics{}, fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 17 {
		return Metrics{}, fmt.Errorf("invalid proc stat fields")
	}
	user, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return Metrics{}, err
	}
	system, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return Metrics{}, err
	}
	threads, err := strconv.ParseUint(fields[17], 10, 32)
	if err != nil {
		return Metrics{}, err
	}
	status, err := os.Open(filepath.Join("/proc", strconv.Itoa(identity.PID), "status"))
	if err != nil {
		return Metrics{}, err
	}
	defer status.Close()
	var rss uint64
	scanner := bufio.NewScanner(status)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			_, _ = fmt.Sscanf(line, "VmRSS: %d kB", &rss)
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return Metrics{}, err
	}
	// Linux exposes clock ticks via sysconf; 100 is the POSIX default on the
	// supported targets and keeps this dependency-free for the native runtime.
	return Metrics{CPUTime: time.Duration((user + system) * uint64(time.Second) / 100), MemoryBytes: rss * 1024, ThreadCount: uint32(threads)}, nil
}
