package filesystem

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("client disconnected") }

func testSandbox(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(), root
}

func TestResolveServerPath(t *testing.T) {
	service, root := testSandbox(t)
	resolved, err := service.ResolveServerPath(root, "nested\\file.txt")
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}

	for _, value := range []string{
		"../outside.txt",
		"..\\outside.txt",
		"nested/../../outside.txt",
		filepath.Join(filepath.Dir(root), "outside.txt"),
		"C:\\Windows\\win.ini",
		"\\\\server\\share\\file.txt",
		"../" + filepath.Base(root) + "-evil/file.txt",
	} {
		if _, err := service.ResolveServerPath(root, value); !errors.Is(err, ErrPathEscapesRoot) {
			t.Errorf("ResolveServerPath(%q) error = %v, want path escape", value, err)
		}
	}
}

func TestResolveServerPathSecurityMatrix(t *testing.T) {
	service, root := testSandbox(t)
	outsideRoot := root + "-evil"
	if err := os.Mkdir(outsideRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideRoot, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{
		"../outside.txt",
		"..\\outside.txt",
		"nested/../../outside.txt",
		"nested\\..\\..\\outside.txt",
		"/etc/passwd",
		"C:\\Windows\\win.ini",
		"\\\\server\\share\\file.txt",
		filepath.Join(outsideRoot, "secret.txt"),
	} {
		if _, err := service.ResolveServerPath(root, value); !errors.Is(err, ErrPathEscapesRoot) {
			t.Errorf("ResolveServerPath(%q) error = %v, want path escape", value, err)
		}
	}

	resolved, err := service.ResolveServerPath(root, "nested\\file.txt")
	want, wantErr := filepath.EvalSymlinks(filepath.Join(root, "nested", "file.txt"))
	if wantErr != nil {
		t.Fatal(wantErr)
	}
	if err != nil || resolved != want {
		t.Fatalf("mixed separator safe path = %q, %v", resolved, err)
	}
}

func TestListReadStatAndLimits(t *testing.T) {
	service, root := testSandbox(t)
	if err := os.Mkdir(filepath.Join(root, "a-directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "z-file.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := service.ListDirectory(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Name != "a-directory" || entries[0].Type != "directory" || entries[1].Name != "nested" || entries[2].Name != "z-file.txt" {
		t.Fatalf("unexpected directory entries: %#v", entries)
	}
	content, err := service.ReadFile(root, "nested/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if content.Content != "hello" || content.Encoding != "utf-8" || content.RelativePath != "nested/file.txt" {
		t.Fatalf("unexpected content: %#v", content)
	}
	if _, err := service.Stat(root, "nested/file.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadFile(root, "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file error = %v", err)
	}
	if _, err := service.ListDirectory(root, "nested/file.txt"); !errors.Is(err, ErrExpectedDir) {
		t.Fatalf("file as directory error = %v", err)
	}
	if _, err := service.ReadFile(root, "nested"); !errors.Is(err, ErrExpectedFile) {
		t.Fatalf("directory as file error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), make([]byte, MaxReadBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadFile(root, "large.txt"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large file error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadFile(root, "binary.bin"); !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("binary file error = %v", err)
	}
}

func TestDirectoryEntryLimit(t *testing.T) {
	service, root := testSandbox(t)
	for i := 0; i <= MaxDirectoryEntries; i++ {
		name := filepath.Join(root, fmt.Sprintf("entry-%05d", i))
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.ListDirectory(root, ""); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("entry limit error = %v", err)
	}
}

func TestMutationsStayWithinRoot(t *testing.T) {
	service, root := testSandbox(t)
	if err := service.CreateFile(root, "new.txt", "initial"); err != nil {
		t.Fatal(err)
	}
	if content, err := service.ReadFile(root, "new.txt"); err != nil || content.Content != "initial" {
		t.Fatalf("created content=%#v err=%v", content, err)
	}
	if err := service.CreateFile(root, "new.txt", "again"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("existing create error = %v", err)
	}
	if err := service.CreateDirectory(root, "created"); err != nil {
		t.Fatal(err)
	}
	if err := service.CreateDirectory(root, "created"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("existing directory error = %v", err)
	}
	if err := service.CreateFile(root, "../outside.txt", "no"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("create traversal error = %v", err)
	}
	if err := service.CreateDirectory(root, "missing/child"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nested directory without parent error = %v", err)
	}

	original := filepath.Join(root, "new.txt")
	if err := os.Chmod(original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.WriteFile(root, "new.txt", "updated"); err != nil {
		t.Fatal(err)
	}
	content, err := service.ReadFile(root, "new.txt")
	if err != nil || content.Content != "updated" {
		t.Fatalf("updated content=%#v err=%v", content, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(original)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("write did not preserve mode: info=%#v err=%v", info, err)
		}
	}
	if matches, err := filepath.Glob(filepath.Join(root, ".gamenode-write-*")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary write files remain: %v %v", matches, err)
	}
	if err := service.WriteFile(root, "../outside.txt", "no"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("write traversal error = %v", err)
	}
	if err := service.WriteFile(root, "new.txt", string(make([]byte, MaxReadBytes+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large write error = %v", err)
	}
}

func TestMoveAndDelete(t *testing.T) {
	service, root := testSandbox(t)
	if err := os.Mkdir(filepath.Join(root, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "destination"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "file.txt"), []byte("move"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.Move(root, "source/file.txt", "source/renamed.txt"); err != nil {
		t.Fatal(err)
	}
	if err := service.Move(root, "source/renamed.txt", "destination/moved.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "destination", "moved.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "destination", "conflict.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.Move(root, "destination/moved.txt", "destination/conflict.txt"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("move conflict error = %v", err)
	}
	if err := service.Move(root, "../outside", "destination/outside"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("move source traversal error = %v", err)
	}
	if err := service.Move(root, "destination/moved.txt", "../outside"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("move destination traversal error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tree", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := service.Move(root, "tree", "tree/child/tree"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("move into itself error = %v", err)
	}

	if err := service.Delete(root, "destination/moved.txt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "destination", "moved.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file remained after delete: %v", err)
	}
	if err := service.Delete(root, "empty", false); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(root, "tree", false); !errors.Is(err, ErrDirectoryNotEmpty) {
		t.Fatalf("non-recursive delete error = %v", err)
	}
	if err := service.Delete(root, "tree", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "tree")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tree remained after recursive delete: %v", err)
	}
	if err := service.Delete(root, ".", true); !errors.Is(err, ErrRootOperation) {
		t.Fatalf("root delete error = %v", err)
	}
}

func TestMoveManagedRoot(t *testing.T) {
	service := New()
	dataRoot := t.TempDir()
	source := filepath.Join(dataRoot, "tenants", "default", "servers", "game")
	destination := filepath.Join(dataRoot, "tenants", "destination", "servers", "game")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "world.dat"), []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.MoveManagedRoot(dataRoot, source, destination); err != nil {
		t.Fatalf("MoveManagedRoot() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "world.dat")); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if err := service.MoveManagedRoot(dataRoot, filepath.Join(dataRoot, "tenants", "destination", "servers", "game"), filepath.Join(dataRoot, "tenants", "other", "servers", "game")); err != nil {
		t.Fatalf("second MoveManagedRoot() error = %v", err)
	}
	if err := service.MoveManagedRoot(dataRoot, filepath.Join(dataRoot, "tenants", "other", "servers", "game"), filepath.Join(dataRoot, "tenants", "other", "servers", "game")); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("same-root move error = %v", err)
	}
}

func TestUploadAndDownloadStreamWithoutPartialFiles(t *testing.T) {
	service, root := testSandbox(t)
	service = New(Options{MaxUploadBytes: 3 << 20})
	payload := bytes.Repeat([]byte{0, 1, 2, 3}, 512<<10)
	info, err := service.Upload(root, "", "blob.bin", bytes.NewReader(payload), false)
	if err != nil || info.Size != int64(len(payload)) || info.RelativePath != "blob.bin" {
		t.Fatalf("upload info=%#v err=%v", info, err)
	}
	file, downloaded, err := service.OpenDownload(root, "blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || downloaded.Size != int64(len(payload)) || !bytes.Equal(data, payload) {
		t.Fatalf("download mismatch: read=%v close=%v size=%d", readErr, closeErr, downloaded.Size)
	}
	if _, err := service.Upload(root, "", "blob.bin", bytes.NewReader(payload), false); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("upload conflict error = %v", err)
	}
	if _, err := service.Upload(root, "", "blob.bin", bytes.NewReader([]byte("replacement")), true); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "blob.bin")); err != nil || string(got) != "replacement" {
		t.Fatalf("overwrite content=%q err=%v", got, err)
	}
	for _, filename := range []string{"../outside.bin", "nested/outside.bin", "nested\\outside.bin", "C:\\outside.bin", "\\\\host\\share.bin"} {
		if _, err := service.Upload(root, "", filename, bytes.NewReader(payload), false); !errors.Is(err, ErrInvalidFilename) {
			t.Fatalf("unsafe filename %q error = %v", filename, err)
		}
	}
	if _, err := service.Upload(root, "../outside", "safe.bin", bytes.NewReader(payload), false); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("unsafe target directory error = %v", err)
	}
	limited := New(Options{MaxUploadBytes: 16})
	if _, err := limited.Upload(root, "", "too-large.bin", bytes.NewReader(make([]byte, 17)), false); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized upload error = %v", err)
	}
	if _, err := service.Upload(root, "", "aborted.bin", failingReader{}, false); err == nil {
		t.Fatal("aborted upload unexpectedly succeeded")
	}
	if _, err := os.Lstat(filepath.Join(root, "aborted.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial target exists: %v", err)
	}
	if temporary, err := filepath.Glob(filepath.Join(root, ".gamenode-upload-*")); err != nil || len(temporary) != 0 {
		t.Fatalf("upload temporary files remain: %v %v", temporary, err)
	}
	if _, _, err := service.OpenDownload(root, "nested"); !errors.Is(err, ErrExpectedFile) {
		t.Fatalf("directory download error = %v", err)
	}
}
