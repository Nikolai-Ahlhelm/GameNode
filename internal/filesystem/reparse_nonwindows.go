//go:build !windows

package filesystem

func isReparsePoint(string) (bool, error) { return false, nil }
