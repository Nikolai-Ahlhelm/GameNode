package tenants_test

import (
	"path/filepath"
	"testing"

	"gamenode/internal/tenants"
)

func TestTenantServerRootValidPath(t *testing.T) {
	dataRoot := t.TempDir()
	got, err := tenants.TenantServerRoot(dataRoot, "tenant-a", "minecraft")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dataRoot, "tenants", "tenant-a", "servers", "minecraft")
	if got != want {
		t.Fatalf("TenantServerRoot() = %q, want %q", got, want)
	}
}

func TestTenantServerRootSameDirectoryAcrossTenantsDiffers(t *testing.T) {
	dataRoot := t.TempDir()
	a, err := tenants.TenantServerRoot(dataRoot, "tenant-a", "minecraft")
	if err != nil {
		t.Fatal(err)
	}
	b, err := tenants.TenantServerRoot(dataRoot, "tenant-b", "minecraft")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("expected different roots for different tenants, both = %q", a)
	}
}

func TestTenantServerRootSameDirectorySameTenantMatches(t *testing.T) {
	dataRoot := t.TempDir()
	a, err := tenants.TenantServerRoot(dataRoot, "tenant-a", "minecraft")
	if err != nil {
		t.Fatal(err)
	}
	b, err := tenants.TenantServerRoot(dataRoot, "tenant-a", "minecraft")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("expected identical roots for the same tenant+directory: %q != %q", a, b)
	}
}

// TestTenantServerRootRejectsInvalidDirectoryNames covers traversal,
// absolute paths, drive-qualified paths, UNC paths, and mixed-separator
// traversal for directoryName. The backslash-based attack strings are
// literal Go string content (not OS path syntax) and are rejected on every
// platform because validateStorageSegment checks for the '\\' character
// directly rather than relying on OS separator semantics.
func TestTenantServerRootRejectsInvalidDirectoryNames(t *testing.T) {
	dataRoot := t.TempDir()
	for _, name := range []string{
		"",
		".",
		"..",
		"../escape",
		"..\\escape",
		"foo/../bar",
		"foo\\..\\bar",
		"/etc/passwd",
		`C:\Windows`,
		`\\server\share`,
		"foo/bar",
		"foo\\bar",
		"name\x00null",
		"name\x01control",
	} {
		if _, err := tenants.TenantServerRoot(dataRoot, "tenant-a", name); err == nil {
			t.Fatalf("TenantServerRoot(directory=%q) accepted an unsafe directory name", name)
		}
	}
}

func TestTenantServerRootRejectsInvalidTenantIDs(t *testing.T) {
	dataRoot := t.TempDir()
	for _, id := range []string{
		"",
		".",
		"..",
		"../escape",
		"/etc",
		`C:\`,
		`\\server\share`,
		"tenant/a",
		"tenant\\a",
		"tenant\x00null",
	} {
		if _, err := tenants.TenantServerRoot(dataRoot, id, "minecraft"); err == nil {
			t.Fatalf("TenantServerRoot(tenant=%q) accepted an unsafe tenant id", id)
		}
	}
}

func TestTenantServerRootRejectsEmptyOrRelativeDataRoot(t *testing.T) {
	if _, err := tenants.TenantServerRoot("", "tenant-a", "minecraft"); err == nil {
		t.Fatal("empty data root accepted")
	}
	if _, err := tenants.TenantServerRoot("relative/data", "tenant-a", "minecraft"); err == nil {
		t.Fatal("relative data root accepted")
	}
}

func TestTenantServerRootBoundary(t *testing.T) {
	dataRoot := t.TempDir()
	root, err := tenants.TenantServerRoot(dataRoot, "default", "seven")
	if err != nil {
		t.Fatal(err)
	}
	serversRoot := filepath.Join(dataRoot, "tenants", "default", "servers")
	relative, err := filepath.Rel(serversRoot, root)
	if err != nil || relative != "seven" {
		t.Fatalf("resolved root escaped its tenant/servers boundary: root=%q relative=%q err=%v", root, relative, err)
	}
}
