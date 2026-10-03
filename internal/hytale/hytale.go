// Package hytale holds Hytale-specific, transport-free pieces. GameNode does not
// install Hytale: its server files are only distributed through Hytale's own
// downloader, which requires an interactive account login (an account-login
// flow GameNode deliberately does not implement). A server is therefore adopted
// from a directory the operator installed, and this package only provides the
// mod archive profile. See docs/adr/0014-vintage-story-and-hytale.md.
package hytale

import (
	"archive/zip"
	"encoding/json"

	"gamenode/internal/filesystem"
	"gamenode/internal/mods"
)

// ModsDirectory is the server-root-relative mod directory.
const ModsDirectory = "mods"

// ModProfile is the Hytale mod layout: plugin .jar or pack .zip archives in
// <root>/mods, identified from the manifest.json at the archive root.
var ModProfile = mods.Profile{Game: "hytale", Directory: ModsDirectory, Extensions: []string{".jar", ".zip"}, Metadata: readManifest}

// NewModManager builds a mod manager over the shared filesystem service.
func NewModManager(files *filesystem.Service) *mods.Manager {
	return mods.NewManager(files, ModProfile)
}

// readManifest reads the documented manifest.json keys. Hytale's serializer is
// case-sensitive and keys start with an upper-case letter.
func readManifest(archive *zip.Reader, mod *mods.Mod) {
	text := mods.ReadEntry(archive, "manifest.json")
	if text == "" {
		return
	}
	var manifest struct {
		Group       string `json:"Group"`
		Name        string `json:"Name"`
		Version     string `json:"Version"`
		Description string `json:"Description"`
	}
	if json.Unmarshal([]byte(text), &manifest) != nil {
		return
	}
	id := manifest.Name
	if manifest.Group != "" && manifest.Name != "" {
		id = manifest.Group + ":" + manifest.Name
	}
	mods.Fill(mod, "", id, manifest.Name, manifest.Version, manifest.Description)
}
