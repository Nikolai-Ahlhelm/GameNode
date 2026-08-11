//go:build windows

package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsSymlinkEscapeIsRejected(t *testing.T) {
	service, root := testSandbox(t)
	inside := filepath.Join(root, "nested", "file.txt")
	insideLink := filepath.Join(root, "inside-link")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Skipf("creating test symlink is unavailable: %v", err)
	}
	if _, err := service.ReadFile(root, "inside-link"); !errors.Is(err, ErrSpecialFile) {
		t.Fatalf("Windows inside reparse-point error = %v", err)
	}
	if err := os.Remove(insideLink); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("creating test symlink is unavailable: %v", err)
	}
	if _, err := service.ReadFile(root, "outside-link"); !errors.Is(err, ErrSpecialFile) {
		t.Fatalf("Windows reparse-point error = %v", err)
	}
	if err := service.CreateFile(root, "outside-link/created.txt", "no"); !errors.Is(err, ErrSpecialFile) {
		t.Fatalf("Windows reparse-point mutation error = %v", err)
	}
	if err := service.Move(root, "nested/file.txt", "outside-link/moved.txt"); !errors.Is(err, ErrSpecialFile) {
		t.Fatalf("Windows reparse-point move destination error = %v", err)
	}
}
