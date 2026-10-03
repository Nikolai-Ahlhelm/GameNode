package minecraft

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"gamenode/internal/filesystem"
)

const (
	// ModsDirectory is the server-root-relative directory that holds mod jars.
	ModsDirectory = "mods"

	maxListedMods     = 2000
	maxMetadataEntry  = 256 << 10
	maxMetadataJarLen = 1 << 30
)

var (
	ErrInvalidModFile = errors.New("mod must be a .jar file with a safe file name")
	ErrNotAJar        = errors.New("uploaded file is not a valid jar archive")
	ErrModNotFound    = errors.New("mod not found")

	modFileNamePattern = regexp.MustCompile(`(?i)^[A-Za-z0-9][A-Za-z0-9 ._+()\[\]-]{0,150}\.jar$`)
	// ErrModState reports an enable/disable request the file name cannot express.
	ErrModState = errors.New("mod is not in a state that allows this change")
)

// DisabledSuffix marks a mod jar that stays installed but is not loaded: the
// loaders only scan *.jar, so renaming to *.jar.disabled hides it from them.
const DisabledSuffix = ".disabled"

// Mod describes one jar in the server's mods directory. Metadata fields are
// best-effort and untrusted: they come from the jar itself.
type Mod struct {
	FileName    string    `json:"file_name"`
	Size        int64     `json:"size"`
	ModifiedAt  time.Time `json:"modified_at"`
	ID          string    `json:"id,omitempty"`
	Name        string    `json:"name,omitempty"`
	Version     string    `json:"version,omitempty"`
	Description string    `json:"description,omitempty"`
	Loaders     []string  `json:"loaders,omitempty"`
	// Disabled is true for a *.jar.disabled file: installed but not loaded.
	Disabled bool `json:"disabled,omitempty"`
}

// ModManager lists, adds and removes mod jars. All path handling is delegated
// to internal/filesystem, so the server-root sandbox, atomic upload commit and
// reparse-point rules apply unchanged.
type ModManager struct{ files *filesystem.Service }

// NewModManager builds a ModManager over the shared filesystem service.
func NewModManager(files *filesystem.Service) *ModManager { return &ModManager{files: files} }

// ValidModFileName reports whether name is an acceptable (enabled) mod jar file name.
func ValidModFileName(name string) bool { return modFileNamePattern.MatchString(name) }

// ValidModEntryName accepts an enabled "x.jar" or a disabled "x.jar.disabled".
func ValidModEntryName(name string) bool {
	return ValidModFileName(name) || (strings.HasSuffix(name, DisabledSuffix) && ValidModFileName(strings.TrimSuffix(name, DisabledSuffix)))
}

// List returns the mod jars in root's mods directory, sorted by file name. A
// missing mods directory is an empty list, not an error.
func (m *ModManager) List(root string) ([]Mod, error) {
	entries, err := m.files.ListDirectory(root, ModsDirectory)
	if err != nil {
		if errors.Is(err, filesystem.ErrNotFound) {
			return []Mod{}, nil
		}
		return nil, err
	}
	mods := make([]Mod, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "file" || !ValidModEntryName(entry.Name) {
			continue
		}
		if len(mods) >= maxListedMods {
			break
		}
		mod := Mod{FileName: entry.Name, Size: entry.Size, ModifiedAt: entry.ModifiedAt, Disabled: strings.HasSuffix(entry.Name, DisabledSuffix)}
		m.readMetadata(root, &mod)
		mods = append(mods, mod)
	}
	sort.Slice(mods, func(left, right int) bool {
		return strings.ToLower(mods[left].FileName) < strings.ToLower(mods[right].FileName)
	})
	return mods, nil
}

// Add stores an uploaded jar in the mods directory. The stream must begin with
// the ZIP signature; the committed file must also parse as a jar archive.
func (m *ModManager) Add(root, fileName string, data io.Reader, overwrite bool) (Mod, error) {
	if !ValidModFileName(fileName) {
		return Mod{}, ErrInvalidModFile
	}
	reader := bufio.NewReader(data)
	magic, err := reader.Peek(4)
	if err != nil || !bytes.Equal(magic, []byte("PK\x03\x04")) {
		return Mod{}, ErrNotAJar
	}
	if err = m.ensureDirectory(root); err != nil {
		return Mod{}, err
	}
	info, err := m.files.Upload(root, ModsDirectory, fileName, reader, overwrite)
	if err != nil {
		return Mod{}, err
	}
	mod := Mod{FileName: fileName, Size: info.Size, ModifiedAt: info.ModifiedAt}
	if !m.readMetadata(root, &mod) {
		_ = m.files.Delete(root, ModsDirectory+"/"+fileName, false)
		return Mod{}, ErrNotAJar
	}
	return mod, nil
}

// SetEnabled enables or disables a mod by renaming it between x.jar and
// x.jar.disabled inside the mods directory. fileName is the current on-disk
// name. The mod stays installed either way; a disabled mod is simply not
// matched by the loader's *.jar scan. Requesting the state a mod is already in
// returns it unchanged.
func (m *ModManager) SetEnabled(root, fileName string, enabled bool) (Mod, error) {
	if !ValidModEntryName(fileName) {
		return Mod{}, ErrInvalidModFile
	}
	currentlyDisabled := strings.HasSuffix(fileName, DisabledSuffix)
	target := fileName
	switch {
	case enabled && currentlyDisabled:
		target = strings.TrimSuffix(fileName, DisabledSuffix)
	case !enabled && !currentlyDisabled:
		target = fileName + DisabledSuffix
	}
	if target != fileName {
		if err := m.files.Move(root, ModsDirectory+"/"+fileName, ModsDirectory+"/"+target); err != nil {
			if errors.Is(err, filesystem.ErrNotFound) {
				return Mod{}, ErrModNotFound
			}
			return Mod{}, err
		}
	}
	info, err := m.files.Stat(root, ModsDirectory+"/"+target)
	if err != nil {
		if errors.Is(err, filesystem.ErrNotFound) {
			return Mod{}, ErrModNotFound
		}
		return Mod{}, err
	}
	mod := Mod{FileName: target, Size: info.Size, ModifiedAt: info.ModifiedAt, Disabled: strings.HasSuffix(target, DisabledSuffix)}
	m.readMetadata(root, &mod)
	return mod, nil
}

// Remove deletes one mod jar, enabled or disabled. Directories and other files
// are never touched.
func (m *ModManager) Remove(root, fileName string) error {
	if !ValidModEntryName(fileName) {
		return ErrInvalidModFile
	}
	relative := ModsDirectory + "/" + fileName
	if _, err := m.files.Stat(root, relative); err != nil {
		if errors.Is(err, filesystem.ErrNotFound) {
			return ErrModNotFound
		}
		return err
	}
	return m.files.Delete(root, relative, false)
}

func (m *ModManager) ensureDirectory(root string) error {
	if _, err := m.files.ListDirectory(root, ModsDirectory); err == nil {
		return nil
	} else if !errors.Is(err, filesystem.ErrNotFound) {
		return err
	}
	if err := m.files.CreateDirectory(root, ModsDirectory); err != nil && !errors.Is(err, filesystem.ErrAlreadyExists) {
		return err
	}
	return nil
}

// readMetadata fills best-effort metadata and reports whether the file opened
// as a valid zip archive.
func (m *ModManager) readMetadata(root string, mod *Mod) bool {
	file, info, err := m.files.OpenDownload(root, ModsDirectory+"/"+mod.FileName)
	if err != nil {
		return false
	}
	defer file.Close()
	if info.Size > maxMetadataJarLen {
		return true
	}
	archive, err := zip.NewReader(file, info.Size)
	if err != nil {
		return false
	}
	entries := map[string]*zip.File{}
	for _, entry := range archive.File {
		switch entry.Name {
		case "fabric.mod.json", "quilt.mod.json", "META-INF/neoforge.mods.toml", "META-INF/mods.toml", "META-INF/MANIFEST.MF":
			entries[entry.Name] = entry
		}
	}
	read := func(name string) string {
		entry := entries[name]
		if entry == nil || entry.UncompressedSize64 > maxMetadataEntry {
			return ""
		}
		stream, openErr := entry.Open()
		if openErr != nil {
			return ""
		}
		defer stream.Close()
		data, _ := io.ReadAll(io.LimitReader(stream, maxMetadataEntry))
		return string(data)
	}
	if text := read("fabric.mod.json"); text != "" {
		var fabric struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Version     string `json:"version"`
			Description string `json:"description"`
		}
		if json.Unmarshal([]byte(text), &fabric) == nil {
			fill(mod, "fabric", fabric.ID, fabric.Name, fabric.Version, fabric.Description)
		}
	}
	manifestVersion := ""
	if text := read("META-INF/MANIFEST.MF"); text != "" {
		for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
			if value, ok := strings.CutPrefix(line, "Implementation-Version:"); ok {
				manifestVersion = strings.TrimSpace(value)
			}
		}
	}
	for _, candidate := range []struct{ file, loader string }{{"META-INF/neoforge.mods.toml", "neoforge"}, {"META-INF/mods.toml", "forge"}} {
		if text := read(candidate.file); text != "" {
			id, name, version, description := parseModsTOML(text)
			if strings.Contains(version, "${") {
				version = manifestVersion
			}
			fill(mod, candidate.loader, id, name, version, description)
		}
	}
	if text := read("quilt.mod.json"); text != "" {
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
			fill(mod, "quilt", quilt.Loader.ID, quilt.Loader.Metadata.Name, quilt.Loader.Version, quilt.Loader.Metadata.Description)
		}
	}
	return true
}

func fill(mod *Mod, loader, id, name, version, description string) {
	mod.Loaders = append(mod.Loaders, loader)
	if mod.ID == "" {
		mod.ID = clean(id, 100)
	}
	if mod.Name == "" {
		mod.Name = clean(name, 150)
	}
	if mod.Version == "" {
		mod.Version = clean(version, 100)
	}
	if mod.Description == "" {
		mod.Description = clean(description, 300)
	}
}

func clean(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
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
