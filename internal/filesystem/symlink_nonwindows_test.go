//go:build !windows

package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSymlinkResolutionStaysWithinRoot(t *testing.T) {
	service, root := testSandbox(t)
	inside := filepath.Join(root, "nested", "file.txt")
	if err := os.Symlink(inside, filepath.Join(root, "inside-link")); err != nil {
		t.Fatal(err)
	}
	content, err := service.ReadFile(root, "inside-link")
	if err != nil || content.Content != "hello" {
		t.Fatalf("inside symlink content=%#v err=%v", content, err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadFile(root, "outside-link"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("outside symlink error = %v", err)
	}
	if err := os.Symlink("outside-link", filepath.Join(root, "outside-chain")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadFile(root, "outside-chain"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("outside symlink chain error = %v", err)
	}
}

func TestDeleteSymlinkRemovesOnlyLink(t *testing.T) {
	service, root := testSandbox(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(root, "outside-link", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("link still exists: %v", err)
	}
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != "secret" {
		t.Fatalf("outside target changed: %q %v", content, err)
	}
}
