//go:build windows

package filesystem

import "golang.org/x/sys/windows"

func commitUpload(source, destination string, overwrite bool) error {
	if overwrite {
		return atomicReplace(source, destination)
	}
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, 0)
}
