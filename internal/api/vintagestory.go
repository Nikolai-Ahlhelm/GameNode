package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"gamenode/internal/vintagestory"
)

// vintageStoryVersionsHandler lists installable versions for the Vintage Story
// provisioning wizard from the compiled official source. The caller never
// supplies a URL.
func (s *Server) vintageStoryVersionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	// Same gate as browsing provisionable templates.
	if _, _, ok := s.requireGlobalPermission(w, r, "Templates.View", false); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	versions, err := s.vintageStorySource.Versions(ctx)
	if err != nil {
		if errors.Is(err, vintagestory.ErrInvalidVersion) {
			bad(w, "invalid version request")
			return
		}
		errorOut(w, http.StatusBadGateway, "vintagestory_source_unavailable", "the official Vintage Story version source is unavailable")
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"versions": versions, "dotnet": vintageStoryRuntimeInfo(versions)})
}

// vintageStoryRuntimeInfo reports the host's .NET runtimes and, per offered
// version, the advisory .NET major it needs, so the wizard can warn before the
// download starts.
func vintageStoryRuntimeInfo(versions []vintagestory.Version) map[string]any {
	_, found := vintagestory.DiscoverDotnet()
	installed := []int{}
	for major := range vintagestory.InstalledRuntimeMajors() {
		installed = append(installed, major)
	}
	required := map[string]int{}
	for _, version := range versions {
		required[version.Version] = vintagestory.RequiredDotnetMajor(version.Version)
	}
	return map[string]any{"found": found, "installed_majors": installed, "required_major": required}
}
