package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/minecraft"
	"gamenode/internal/mods"
)

// minecraftVersionsHandler lists installable versions for the Minecraft
// provisioning wizard. It reads only the compiled upstream sources; the caller
// supplies a loader and a Minecraft version, never a URL.
func (s *Server) minecraftVersionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	// Same gate as browsing provisionable templates: the wizard that consumes
	// this is part of template provisioning.
	if _, _, ok := s.requireGlobalPermission(w, r, "Templates.View", false); !ok {
		return
	}
	loader := r.URL.Query().Get("loader")
	if !minecraft.ValidLoader(loader) {
		bad(w, "unsupported Minecraft loader")
		return
	}
	version := r.URL.Query().Get("minecraft_version")
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result := map[string]any{"loader": loader}
	games, err := s.minecraftSource.GameVersions(ctx, loader)
	if err != nil {
		minecraftSourceError(w, err)
		return
	}
	result["game_versions"] = games
	if version != "" {
		builds, err := s.minecraftSource.LoaderVersions(ctx, loader, version)
		if err != nil {
			minecraftSourceError(w, err)
			return
		}
		result["loader_versions"] = builds
		_, found := minecraft.DiscoverJava()
		result["java"] = map[string]any{"found": found, "major": minecraft.JavaMajor(), "required_major": minecraft.RequiredJavaMajor(version)}
	}
	jsonOut(w, http.StatusOK, result)
}

func minecraftSourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, minecraft.ErrInvalidVersion), errors.Is(err, minecraft.ErrUnsupportedLoader):
		bad(w, "invalid Minecraft version request")
	case errors.Is(err, minecraft.ErrVersionNotFound):
		errorOut(w, http.StatusNotFound, "minecraft_version_not_found", "the requested version is not offered by the official source")
	default:
		errorOut(w, http.StatusBadGateway, "minecraft_source_unavailable", "the official Minecraft version source is unavailable")
	}
}

type serverModsResponse struct {
	Available bool `json:"available"`
	// Game, Directory and Extensions describe the layout the server's mod
	// manager uses, so the UI can validate files and word its help correctly.
	Game       string          `json:"game,omitempty"`
	Directory  string          `json:"directory,omitempty"`
	Extensions []string        `json:"extensions,omitempty"`
	Loader     string          `json:"loader,omitempty"`
	Mods       []minecraft.Mod `json:"mods"`
	MaxUpload  int64           `json:"max_upload_bytes"`
}

// modsFor selects the mod manager for a server from its creation-time template.
// A server without a recognized template keeps the historical behavior: the
// Minecraft layout, offered only when it already holds mod jars.
func (s *Server) modsFor(ctx context.Context, id string) (manager *mods.Manager, known bool) {
	templateID, _ := s.servers.TemplateID(ctx, id)
	switch {
	case templateID == "vintage-story":
		return s.vintageStoryMods, true
	case templateID == "hytale":
		return s.hytaleMods, true
	case strings.Contains(templateID, "minecraft"):
		return s.mods, true
	}
	return s.mods, false
}

// serverModsHandler manages the mod jars in a server's mods directory. It adds
// no permission of its own: listing needs Files.View, adding Files.Upload and
// removing Files.Delete, exactly what the same change would need through the
// file browser. Mods are only ever added by upload, never from a URL.
func (s *Server) serverModsHandler(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		if _, _, ok := s.requireServerPermission(w, r, "Files.View", id, false); !ok {
			return
		}
		record, err := s.servers.Get(r.Context(), id)
		if err != nil {
			serverError(w, err, false)
			return
		}
		root := record.Server.WorkingDirectory
		manager, known := s.modsFor(r.Context(), id)
		installed, err := manager.List(root)
		if err != nil {
			filesystemError(w, err)
			return
		}
		profile := manager.Profile()
		response := serverModsResponse{Available: known || len(installed) > 0, Game: profile.Game, Directory: profile.Directory, Extensions: profile.Extensions, Mods: installed, MaxUpload: s.files.MaxUploadBytes()}
		if response.Available && profile.Game == "minecraft" {
			response.Loader = minecraft.DetectLoader(root)
		}
		jsonOut(w, http.StatusOK, response)
	case http.MethodPost:
		u, _, ok := s.requireServerPermission(w, r, "Files.Upload", id, true)
		if !ok {
			return
		}
		record, err := s.servers.Get(r.Context(), id)
		if err != nil {
			serverError(w, err, false)
			return
		}
		overwrite, err := parseOverwrite(r)
		if err != nil {
			bad(w, "invalid overwrite parameter")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.files.MaxUploadBytes()+(1<<20))
		reader, err := r.MultipartReader()
		if err != nil {
			bad(w, "multipart form data is required")
			return
		}
		part, err := reader.NextPart()
		if err != nil || part.FormName() != "file" || part.FileName() == "" {
			bad(w, "one file part is required")
			return
		}
		defer part.Close()
		manager, _ := s.modsFor(r.Context(), id)
		mod, err := manager.Add(record.Server.WorkingDirectory, part.FileName(), part, overwrite)
		if err != nil {
			s.recordFileAudit(r, u, audit.FileUpload, audit.Failure, id, "", nil, err)
			modError(w, err)
			return
		}
		relative := manager.Profile().Directory + "/" + mod.FileName
		s.recordFileAudit(r, u, audit.FileUpload, audit.Success, id, relative, map[string]any{"path": relative, "filename": mod.FileName, "size": mod.Size}, nil)
		s.logFileMutation("file.upload", id)
		jsonOut(w, http.StatusCreated, mod)
	case http.MethodPatch:
		// Enabling/disabling renames the jar, so it needs exactly what the file
		// browser needs for a rename.
		u, _, ok := s.requireServerPermission(w, r, "Files.Rename", id, true)
		if !ok {
			return
		}
		record, err := s.servers.Get(r.Context(), id)
		if err != nil {
			serverError(w, err, false)
			return
		}
		var input struct {
			File    string `json:"file"`
			Enabled *bool  `json:"enabled"`
		}
		if !decode(w, r, &input) {
			return
		}
		if input.Enabled == nil {
			bad(w, "enabled is required")
			return
		}
		before := input.File
		manager, _ := s.modsFor(r.Context(), id)
		mod, err := manager.SetEnabled(record.Server.WorkingDirectory, input.File, *input.Enabled)
		if err != nil {
			s.recordFileAudit(r, u, audit.FileRename, audit.Failure, id, "", nil, err)
			modError(w, err)
			return
		}
		if mod.FileName != before {
			from, to := manager.Profile().Directory+"/"+before, manager.Profile().Directory+"/"+mod.FileName
			s.recordFileAudit(r, u, audit.FileRename, audit.Success, id, to, map[string]any{"from": from, "to": to}, nil)
			s.logFileMutation(audit.FileRename, id)
		}
		jsonOut(w, http.StatusOK, mod)
	case http.MethodDelete:
		u, _, ok := s.requireServerPermission(w, r, "Files.Delete", id, true)
		if !ok {
			return
		}
		record, err := s.servers.Get(r.Context(), id)
		if err != nil {
			serverError(w, err, false)
			return
		}
		name := r.URL.Query().Get("file")
		manager, _ := s.modsFor(r.Context(), id)
		if err = manager.Remove(record.Server.WorkingDirectory, name); err != nil {
			s.recordFileAudit(r, u, audit.FileDelete, audit.Failure, id, "", nil, err)
			modError(w, err)
			return
		}
		relative := path.Join(manager.Profile().Directory, name)
		s.recordFileAudit(r, u, audit.FileDelete, audit.Success, id, relative, map[string]any{"path": relative, "recursive": false}, nil)
		s.logFileMutation("file.delete", id)
		w.WriteHeader(http.StatusNoContent)
	default:
		method(w)
	}
}

func modError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, minecraft.ErrInvalidModFile):
		errorOut(w, http.StatusBadRequest, "invalid_mod", "mod must be a .jar file with a safe file name")
	case errors.Is(err, minecraft.ErrNotAJar):
		errorOut(w, http.StatusBadRequest, "invalid_mod_archive", "uploaded file is not a valid jar archive")
	case errors.Is(err, minecraft.ErrModNotFound):
		errorOut(w, http.StatusNotFound, "not_found", "mod not found")
	default:
		filesystemError(w, err)
	}
}
