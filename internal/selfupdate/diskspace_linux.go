package selfupdate

import "syscall"

// freeBytes reports the space available to an unprivileged user on the
// filesystem holding dir.
func freeBytes(dir string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
