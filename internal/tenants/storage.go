package tenants

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// tenantStorageIDPattern is the safe charset for a path component derived
// from a tenant ID. Tenant IDs used here are always opaque, already-issued
// identifiers (see newID and DefaultTenantID) - never a tenant Name or Slug,
// which are display/URL conveniences only and must never become a
// filesystem or security identifier (see GameNode_Tenant_Foundation_Prompt.md).
var tenantStorageIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// directoryStorageNamePattern mirrors internal/provisioning's own
// directoryPattern. It is duplicated here deliberately: TenantServerRoot must
// never assume a caller already validated directoryName and must reject an
// unsafe value on its own.
var directoryStorageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// TenantServerRoot is the single place that builds the managed storage root
// for a newly provisioned/managed server:
//
//	<dataRoot>/tenants/<tenantID>/servers/<directoryName>
//
// tenantID must be a stable, already-issued tenant identifier - never a
// tenant Name or Slug - and directoryName must be a single relative storage
// segment, never a caller-controlled host path. Both are independently
// revalidated here regardless of what an earlier caller already checked, and
// the result is verified to still resolve inside the computed tenant/servers
// boundary before it is returned.
//
// This resolver is used only for managed/provisioned storage. Adopt
// Existing and Custom Application server creation intentionally continue to
// accept an arbitrary, admin-supplied WorkingDirectory through the ordinary
// Create Server path and never call this function; see
// docs/architecture.md's Tenant Foundation Step 2 section.
func TenantServerRoot(dataRoot, tenantID, directoryName string) (string, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return "", errors.New("data root is required")
	}
	root := filepath.Clean(dataRoot)
	if !filepath.IsAbs(root) {
		return "", errors.New("data root must be an absolute path")
	}
	if err := validateStorageSegment(tenantID, tenantStorageIDPattern, "tenant id"); err != nil {
		return "", err
	}
	if err := validateStorageSegment(directoryName, directoryStorageNamePattern, "directory name"); err != nil {
		return "", err
	}
	tenantsRoot := filepath.Join(root, "tenants")
	tenantRoot := filepath.Join(tenantsRoot, tenantID)
	if !isWithin(tenantsRoot, tenantRoot) {
		return "", errors.New("tenant id escapes managed storage")
	}
	serversRoot := filepath.Join(tenantRoot, "servers")
	target := filepath.Join(serversRoot, directoryName)
	if !isWithin(serversRoot, target) {
		return "", errors.New("server target escapes tenant storage")
	}
	return target, nil
}

// validateStorageSegment rejects every case
// GameNode_Tenant_Foundation_Prompt.md calls out explicitly: empty/"."/"..",
// NUL and other control characters, any path separator (which alone also
// covers absolute Unix paths, Windows drive-qualified paths, UNC paths, and
// mixed-separator traversal, since a single safe segment can never contain
// one), an OS-reported absolute path, and a bare drive letter. The pattern
// match is the final allowlist filter.
func validateStorageSegment(value string, pattern *regexp.Regexp, label string) error {
	if value == "" || value == "." || value == ".." {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains an invalid character", label)
		}
	}
	if strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("%s must not contain a path separator", label)
	}
	if filepath.IsAbs(value) {
		return fmt.Errorf("%s must not be an absolute path", label)
	}
	if len(value) >= 2 && value[1] == ':' {
		return fmt.Errorf("%s must not be drive-qualified", label)
	}
	if !pattern.MatchString(value) {
		return fmt.Errorf("%s has an invalid format", label)
	}
	return nil
}

func isWithin(base, target string) bool {
	relative, err := filepath.Rel(base, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
