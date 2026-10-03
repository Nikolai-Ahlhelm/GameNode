package minecraft

import (
	"archive/zip"
	"encoding/json"
	"regexp"
	"strings"

	"gamenode/internal/filesystem"
	"gamenode/internal/mods"
)

// ModsDirectory is the server-root-relative directory that holds mod jars.
const ModsDirectory = "mods"

// DisabledSuffix marks a mod jar that stays installed but is not loaded: the
// loaders only scan *.jar, so renaming to *.jar.disabled hides it from them.
const DisabledSuffix = mods.DisabledSuffix

type (
	// Mod describes one jar in the server's mods directory.
	Mod = mods.Mod
	// ModManager manages the mods directory of a Minecraft server.
	ModManager = mods.Manager
)

var (
	ErrInvalidModFile = mods.ErrInvalidModFile
	ErrNotAJar        = mods.ErrNotAnArchive
	ErrModNotFound    = mods.ErrModNotFound
)

// ModProfile is the Minecraft mod layout: .jar files in <root>/mods, identified
// from Fabric, Quilt, NeoForge or Forge metadata.
var ModProfile = mods.Profile{Game: "minecraft", Directory: ModsDirectory, Extensions: []string{".jar"}, Metadata: readModMetadata}

// NewModManager builds a ModManager over the shared filesystem service.
func NewModManager(files *filesystem.Service) *ModManager { return mods.NewManager(files, ModProfile) }

// ValidModFileName reports whether name is an acceptable (enabled) mod jar file name.
func ValidModFileName(name string) bool { return ModProfile.ValidFileName(name) }

// ValidModEntryName accepts an enabled "x.jar" or a disabled "x.jar.disabled".
func ValidModEntryName(name string) bool { return ModProfile.ValidEntryName(name) }

func readModMetadata(archive *zip.Reader, mod *Mod) {
	if text := mods.ReadEntry(archive, "fabric.mod.json"); text != "" {
		var fabric struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Version     string `json:"version"`
			Description string `json:"description"`
		}
		if json.Unmarshal([]byte(text), &fabric) == nil {
			mods.Fill(mod, "fabric", fabric.ID, fabric.Name, fabric.Version, fabric.Description)
		}
	}
	manifestVersion := ""
	if text := mods.ReadEntry(archive, "META-INF/MANIFEST.MF"); text != "" {
		for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			if value, ok := strings.CutPrefix(line, "Implementation-Version:"); ok {
				manifestVersion = strings.TrimSpace(value)
			}
		}
	}
	for _, candidate := range []struct{ file, loader string }{{"META-INF/neoforge.mods.toml", "neoforge"}, {"META-INF/mods.toml", "forge"}} {
		if text := mods.ReadEntry(archive, candidate.file); text != "" {
			id, name, version, description := parseModsTOML(text)
			if strings.Contains(version, "${") {
				version = manifestVersion
			}
			mods.Fill(mod, candidate.loader, id, name, version, description)
		}
	}
	if text := mods.ReadEntry(archive, "quilt.mod.json"); text != "" {
		var quilt struct {
			Loader struct {
				ID       string `json:"id"`
				Version  string `json:"version"`
				Metadata struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"metadata"`
			} `json:"quilt_loader"`
		}
		if json.Unmarshal([]byte(text), &quilt) == nil {
			mods.Fill(mod, "quilt", quilt.Loader.ID, quilt.Loader.Metadata.Name, quilt.Loader.Version, quilt.Loader.Metadata.Description)
		}
	}
}

var tomlStringLine = regexp.MustCompile(`^\s*([A-Za-z_]+)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|'([^']*)')\s*(?:#.*)?$`)

// parseModsTOML extracts the first [[mods]] table's scalar identity fields. It
// is not a TOML implementation: multi-line strings and nested tables are
// ignored, which is sufficient for modId/version/displayName.
func parseModsTOML(text string) (id, name, version, description string) {
	inMods := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			if inMods && trimmed != "[[mods]]" {
				if id != "" {
					return
				}
			}
			inMods = trimmed == "[[mods]]"
			if inMods && id != "" {
				return
			}
			continue
		}
		if !inMods {
			continue
		}
		match := tomlStringLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		value := match[2]
		if value == "" {
			value = match[3]
		}
		switch match[1] {
		case "modId":
			id = value
		case "version":
			version = value
		case "displayName":
			name = value
		case "description":
			description = value
		}
	}
	return
}
