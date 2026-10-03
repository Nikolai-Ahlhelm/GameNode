// Package mods lists, adds, enables/disables and removes mod archives in a
// game server's mod directory. It is game-neutral: each game supplies a Profile
// (directory, accepted extensions, metadata reader). All path handling is
// delegated to internal/filesystem, so the server-root sandbox, atomic upload
// commit and reparse-point rules apply unchanged. Mods are only ever added by
// upload; nothing here downloads from a URL.
package mods

import (
	"archive/zip"
	"bufio"
	"bytes"
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
	maxListedMods     = 2000
	maxMetadataJarLen = 1 << 30

	// MaxMetadataEntry bounds every metadata file a Profile reads from an archive.
	MaxMetadataEntry = 256 << 10

	// DisabledSuffix marks an archive that stays installed but is not loaded:
	// the games only scan their own extensions, so "x.jar.disabled" is ignored.
	DisabledSuffix = ".disabled"
)

var (
	ErrInvalidModFile = errors.New("mod must be an archive with a supported extension and a safe file name")
	ErrNotAnArchive   = errors.New("uploaded file is not a valid archive")
	ErrModNotFound    = errors.New("mod not found")

	baseNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+()\[\]-]{0,150}$`)
)

// Mod describes one archive in the mod directory. Metadata fields are
// best-effort and untrusted: they come from the archive itself.
type Mod struct {
	FileName    string    `json:"file_name"`
	Size        int64     `json:"size"`
	ModifiedAt  time.Time `json:"modified_at"`
	ID          string    `json:"id,omitempty"`
	Name        string    `json:"name,omitempty"`
	Version     string    `json:"version,omitempty"`
	Description string    `json:"description,omitempty"`
	Loaders     []string  `json:"loaders,omitempty"`
	// Disabled is true for a *.disabled file: installed but not loaded.
	Disabled bool `json:"disabled,omitempty"`
}

// Profile describes one game's mod layout.
type Profile struct {
	// Game is a stable identifier, e.g. "minecraft".
	Game string
	// Directory is the server-root-relative mod directory, e.g. "mods" or "data/Mods".
	Directory string
	// Extensions are the accepted lowercase extensions including the dot.
	Extensions []string
	// Metadata fills best-effort identity fields from the opened archive. It may be nil.
	Metadata func(archive *zip.Reader, mod *Mod)
}

// Manager manages one Profile's directory.
type Manager struct {
	files   *filesystem.Service
	profile Profile
}

// NewManager builds a Manager over the shared filesystem service.
func NewManager(files *filesystem.Service, profile Profile) *Manager {
	return &Manager{files: files, profile: profile}
}

// Profile returns the manager's profile.
func (m *Manager) Profile() Profile { return m.profile }

// ValidFileName reports whether name is an acceptable (enabled) archive name.
func (p Profile) ValidFileName(name string) bool {
	lower := strings.ToLower(name)
	for _, extension := range p.Extensions {
		if strings.HasSuffix(lower, extension) && baseNamePattern.MatchString(name[:len(name)-len(extension)]) {
			return true
		}
	}
	return false
}

// ValidEntryName accepts an enabled "x.ext" or a disabled "x.ext.disabled".
func (p Profile) ValidEntryName(name string) bool {
	return p.ValidFileName(name) || (strings.HasSuffix(name, DisabledSuffix) && p.ValidFileName(strings.TrimSuffix(name, DisabledSuffix)))
}

// List returns the archives in root's mod directory, sorted by file name. A
// missing directory is an empty list, not an error.
func (m *Manager) List(root string) ([]Mod, error) {
	entries, err := m.files.ListDirectory(root, m.profile.Directory)
	if err != nil {
		if errors.Is(err, filesystem.ErrNotFound) {
			return []Mod{}, nil
		}
		return nil, err
	}
	result := make([]Mod, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "file" || !m.profile.ValidEntryName(entry.Name) {
			continue
		}
		if len(result) >= maxListedMods {
			break
		}
		mod := Mod{FileName: entry.Name, Size: entry.Size, ModifiedAt: entry.ModifiedAt, Disabled: strings.HasSuffix(entry.Name, DisabledSuffix)}
		m.readMetadata(root, &mod)
		result = append(result, mod)
	}
	sort.Slice(result, func(left, right int) bool {
		return strings.ToLower(result[left].FileName) < strings.ToLower(result[right].FileName)
	})
	return result, nil
}

// Add stores an uploaded archive. The stream must begin with the ZIP signature
// and the committed file must parse as an archive.
func (m *Manager) Add(root, fileName string, data io.Reader, overwrite bool) (Mod, error) {
	if !m.profile.ValidFileName(fileName) {
		return Mod{}, ErrInvalidModFile
	}
	reader := bufio.NewReader(data)
	magic, err := reader.Peek(4)
	if err != nil || !bytes.Equal(magic, []byte("PK\x03\x04")) {
		return Mod{}, ErrNotAnArchive
	}
	if err = m.ensureDirectory(root); err != nil {
		return Mod{}, err
	}
	info, err := m.files.Upload(root, m.profile.Directory, fileName, reader, overwrite)
	if err != nil {
		return Mod{}, err
	}
	mod := Mod{FileName: fileName, Size: info.Size, ModifiedAt: info.ModifiedAt}
	if !m.readMetadata(root, &mod) {
		_ = m.files.Delete(root, m.profile.Directory+"/"+fileName, false)
		return Mod{}, ErrNotAnArchive
	}
	return mod, nil
}

// SetEnabled enables or disables an archive by renaming it between x.ext and
// x.ext.disabled inside the mod directory. fileName is the current on-disk
// name. Requesting the state it is already in returns it unchanged.
func (m *Manager) SetEnabled(root, fileName string, enabled bool) (Mod, error) {
	if !m.profile.ValidEntryName(fileName) {
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
		if err := m.files.Move(root, m.profile.Directory+"/"+fileName, m.profile.Directory+"/"+target); err != nil {
			if errors.Is(err, filesystem.ErrNotFound) {
				return Mod{}, ErrModNotFound
			}
			return Mod{}, err
		}
	}
	info, err := m.files.Stat(root, m.profile.Directory+"/"+target)
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

// Remove deletes one archive, enabled or disabled. Directories and other files
// are never touched.
func (m *Manager) Remove(root, fileName string) error {
	if !m.profile.ValidEntryName(fileName) {
		return ErrInvalidModFile
	}
	relative := m.profile.Directory + "/" + fileName
	if _, err := m.files.Stat(root, relative); err != nil {
		if errors.Is(err, filesystem.ErrNotFound) {
			return ErrModNotFound
		}
		return err
	}
	return m.files.Delete(root, relative, false)
}

// Exists reports whether the mod directory exists below root.
func (m *Manager) Exists(root string) bool {
	_, err := m.files.ListDirectory(root, m.profile.Directory)
	return err == nil
}

func (m *Manager) ensureDirectory(root string) error {
	if m.Exists(root) {
		return nil
	}
	// Create each missing component so nested profiles such as "data/Mods" work.
	parts := strings.Split(m.profile.Directory, "/")
	for index := range parts {
		prefix := strings.Join(parts[:index+1], "/")
		if _, err := m.files.ListDirectory(root, prefix); err == nil {
			continue
		}
		if err := m.files.CreateDirectory(root, prefix); err != nil && !errors.Is(err, filesystem.ErrAlreadyExists) {
			return err
		}
	}
	return nil
}

// readMetadata fills best-effort metadata and reports whether the file opened
// as a valid zip archive.
func (m *Manager) readMetadata(root string, mod *Mod) bool {
	file, info, err := m.files.OpenDownload(root, m.profile.Directory+"/"+mod.FileName)
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
	if m.profile.Metadata != nil {
		m.profile.Metadata(archive, mod)
	}
	return true
}

// ReadEntry returns the named archive entry's text, bounded by MaxMetadataEntry,
// or "" when it is missing, too large or unreadable. Profiles use it to read
// their metadata files.
func ReadEntry(archive *zip.Reader, name string) string {
	for _, entry := range archive.File {
		if entry.Name != name {
			continue
		}
		if entry.UncompressedSize64 > MaxMetadataEntry {
			return ""
		}
		stream, err := entry.Open()
		if err != nil {
			return ""
		}
		defer stream.Close()
		data, _ := io.ReadAll(io.LimitReader(stream, MaxMetadataEntry))
		return string(data)
	}
	return ""
}

// Fill records one loader's metadata, keeping the first non-empty value of each field.
func Fill(mod *Mod, loader, id, name, version, description string) {
	if loader != "" {
		mod.Loaders = append(mod.Loaders, loader)
	}
	if mod.ID == "" {
		mod.ID = Clean(id, 100)
	}
	if mod.Name == "" {
		mod.Name = Clean(name, 150)
	}
	if mod.Version == "" {
		mod.Version = Clean(version, 100)
	}
	if mod.Description == "" {
		mod.Description = Clean(description, 300)
	}
}

// Clean strips control characters and bounds untrusted metadata text.
func Clean(value string, limit int) string {
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
