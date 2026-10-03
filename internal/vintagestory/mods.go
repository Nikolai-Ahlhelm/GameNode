package vintagestory

import (
	"archive/zip"
	"regexp"

	"gamenode/internal/filesystem"
	"gamenode/internal/mods"
)

// ModsDirectory is the server-root-relative mod directory (below --dataPath).
const ModsDirectory = DataDirectory + "/Mods"

// ModProfile is the Vintage Story mod layout: .zip archives in <data>/Mods,
// identified from the modinfo.json at the archive root.
var ModProfile = mods.Profile{Game: "vintagestory", Directory: ModsDirectory, Extensions: []string{".zip"}, Metadata: readModInfo}

// NewModManager builds a mod manager over the shared filesystem service.
func NewModManager(files *filesystem.Service) *mods.Manager {
	return mods.NewManager(files, ModProfile)
}

// modinfo.json is authored for Newtonsoft and is often not strict JSON
// (comments, trailing commas, unquoted keys), so identity fields are read with
// bounded patterns instead of a strict parser.
var modInfoField = func(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?is)["']?` + key + `["']?\s*:\s*"((?:[^"\\]|\\.)*)"`)
}

var (
	modIDField          = modInfoField("modid")
	modNameField        = modInfoField("name")
	modVersionField     = modInfoField("version")
	modDescriptionField = modInfoField("description")
)

func readModInfo(archive *zip.Reader, mod *mods.Mod) {
	text := mods.ReadEntry(archive, "modinfo.json")
	if text == "" {
		return
	}
	pick := func(pattern *regexp.Regexp) string {
		if match := pattern.FindStringSubmatch(text); match != nil {
			return match[1]
		}
		return ""
	}
	mods.Fill(mod, "", pick(modIDField), pick(modNameField), pick(modVersionField), pick(modDescriptionField))
}
