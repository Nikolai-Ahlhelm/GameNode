package api

import (
	"testing"

	"gamenode/internal/rbac"
)

func TestProductCapabilitiesCoverPermissionCatalog(t *testing.T) {
	available := make(map[string]bool, len(productPermissions))
	for _, permission := range productPermissions {
		available[permission] = true
	}
	for _, permission := range rbac.Catalog {
		if !available[permission.Key] {
			t.Errorf("catalog permission %q is missing from API capabilities", permission.Key)
		}
	}
}
