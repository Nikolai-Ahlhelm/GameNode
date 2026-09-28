//go:build !windows && !linux

package selfupdate

import "errors"

func freeBytes(string) (uint64, error) {
	return 0, errors.New("free space is not measurable on this platform")
}
