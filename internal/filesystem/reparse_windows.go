//go:build windows

package filesystem

import "golang.org/x/sys/windows"

// Windows junctions and other reparse points do not all have safe, portable
// target resolution semantics. 4A denies them conservatively.
func isReparsePoint(name string) (bool, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(path)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
