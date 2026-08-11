//go:build !windows

package filesystem

import "os"

func commitUpload(source, destination string, overwrite bool) error {
	if overwrite {
		return atomicReplace(source, destination)
	}
	if err := os.Link(source, destination); err != nil {
		return err
	}
	return os.Remove(source)
}
