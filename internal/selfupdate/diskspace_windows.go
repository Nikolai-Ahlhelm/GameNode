package selfupdate

import "golang.org/x/sys/windows"

// freeBytes reports the space available to the current user on the volume
// holding dir.
func freeBytes(dir string) (uint64, error) {
	pointer, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &available, &total, &free); err != nil {
		return 0, err
	}
	return available, nil
}
