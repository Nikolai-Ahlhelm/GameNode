// Package filesystem provides the server-root sandbox used by file APIs.
package filesystem

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxPathBytes              = 4096
	MaxReadBytes        int64 = 4 << 20
	MaxDirectoryEntries       = 10000
)

var (
	ErrInvalidPath       = errors.New("invalid filesystem path")
	ErrPathEscapesRoot   = errors.New("path escapes server root")
	ErrNotFound          = errors.New("path not found")
	ErrExpectedFile      = errors.New("expected regular file")
	ErrExpectedDir       = errors.New("expected directory")
	ErrSpecialFile       = errors.New("special filesystem object")
	ErrTooLarge          = errors.New("file exceeds read limit")
	ErrBinaryFile        = errors.New("binary file is not supported")
	ErrAlreadyExists     = errors.New("filesystem object already exists")
	ErrRootOperation     = errors.New("server root cannot be mutated")
	ErrDirectoryNotEmpty = errors.New("directory is not empty")
	ErrInvalidFilename   = errors.New("invalid upload filename")
)

const DefaultMaxUploadBytes int64 = 64 << 20

type Options struct {
	MaxUploadBytes int64
}

// Entry is a safe, root-relative directory entry. RelativePath always uses
// forward slashes so API clients receive platform-neutral paths.
type Entry struct {
	Name         string    `json:"name"`
	RelativePath string    `json:"path"`
	Type         string    `json:"type"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modified_at"`
	IsSymlink    bool      `json:"is_symlink,omitempty"`
}

type FileInfo struct {
	RelativePath string    `json:"path"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"modified_at"`
}

type Content struct {
	FileInfo
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// Service deliberately owns all path-security logic so API and business code
// do not independently interpret client-supplied paths.
type Service struct{ maxUploadBytes int64 }

func New(options ...Options) *Service {
	maxUploadBytes := DefaultMaxUploadBytes
	if len(options) > 0 && options[0].MaxUploadBytes > 0 {
		maxUploadBytes = options[0].MaxUploadBytes
	}
	return &Service{maxUploadBytes: maxUploadBytes}
}

func (s *Service) MaxUploadBytes() int64 { return s.maxUploadBytes }

func (s *Service) ResolveServerPath(root, relativePath string) (string, error) {
	sandbox, err := newSandbox(root)
	if err != nil {
		return "", err
	}
	return sandbox.resolve(relativePath)
}

// ResolveServerMutationPath validates a root-relative mutation target while
// allowing the final path component not to exist. It is exposed for trusted
// protocol adapters such as the embedded FTP service; callers must still use
// the returned absolute path directly and must never append client input.
func (s *Service) ResolveServerMutationPath(root, relativePath string) (string, error) {
	sandbox, err := newSandbox(root)
	if err != nil {
		return "", err
	}
	return sandbox.mutationPath(relativePath)
}

func (s *Service) ListDirectory(root, relativePath string) ([]Entry, error) {
	sandbox, err := newSandbox(root)
	if err != nil {
		return nil, err
	}
	return sandbox.list(relativePath)
}

func (s *Service) Stat(root, relativePath string) (FileInfo, error) {
	sandbox, err := newSandbox(root)
	if err != nil {
		return FileInfo{}, err
	}
	return sandbox.stat(relativePath)
}

func (s *Service) ReadFile(root, relativePath string) (Content, error) {
	sandbox, err := newSandbox(root)
	if err != nil {
		return Content{}, err
	}
	return sandbox.readFile(relativePath)
}

func (s *Service) CreateFile(root, relativePath, content string) error {
	sandbox, err := newSandbox(root)
	if err != nil {
		return err
	}
	return sandbox.createFile(relativePath, content)
}

func (s *Service) CreateDirectory(root, relativePath string) error {
	sandbox, err := newSandbox(root)
	if err != nil {
		return err
	}
	return sandbox.createDirectory(relativePath)
}

func (s *Service) WriteFile(root, relativePath, content string) error {
	sandbox, err := newSandbox(root)
	if err != nil {
		return err
	}
	return sandbox.writeFile(relativePath, content)
}

func (s *Service) Move(root, source, destination string) error {
	sandbox, err := newSandbox(root)
	if err != nil {
		return err
	}
	return sandbox.move(source, destination)
}

// MoveManagedRoot atomically moves one complete managed server directory
// below the GameNode data root. Both paths are resolved through the same
// sandbox boundary and reparse-point checks as ordinary server-file moves;
// callers must provide paths that have already been derived from validated
// tenant storage identifiers. The destination parent is created when needed.
// Cross-filesystem moves are deliberately not copied implicitly: callers get
// the underlying rename error so a partial, unbounded copy can never occur.
func (s *Service) MoveManagedRoot(dataRoot, source, destination string) error {
	sandbox, err := newSandbox(dataRoot)
	if err != nil {
		return err
	}
	if filepath.Clean(source) == filepath.Clean(destination) {
		return ErrInvalidPath
	}
	relativeRoot := sandbox.root
	relativeSource := filepath.Clean(source)
	relativeDestination := filepath.Clean(destination)
	if filepath.Separator == '\\' {
		relativeRoot = strings.ToLower(relativeRoot)
		relativeSource = strings.ToLower(relativeSource)
		relativeDestination = strings.ToLower(relativeDestination)
	}
	sourceRelative, err := filepath.Rel(relativeRoot, relativeSource)
	if err != nil || sourceRelative == "." || filepath.IsAbs(sourceRelative) || strings.HasPrefix(sourceRelative, ".."+string(filepath.Separator)) || sourceRelative == ".." {
		return ErrPathEscapesRoot
	}
	destinationRelative, err := filepath.Rel(relativeRoot, relativeDestination)
	if err != nil || destinationRelative == "." || filepath.IsAbs(destinationRelative) || strings.HasPrefix(destinationRelative, ".."+string(filepath.Separator)) || destinationRelative == ".." {
		return ErrPathEscapesRoot
	}
	sourcePath, err := sandbox.mutationPath(filepath.ToSlash(sourceRelative))
	if err != nil {
		return err
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return mapPathError(err)
	}
	if !sourceInfo.IsDir() {
		return ErrExpectedDir
	}
	if unsafe, err := isReparsePoint(sourcePath); err != nil {
		return mapPathError(err)
	} else if unsafe {
		return ErrSpecialFile
	}
	destinationParent := filepath.Dir(filepath.Clean(destination))
	if err := os.MkdirAll(destinationParent, 0o700); err != nil {
		return mapPathError(err)
	}
	destinationPath, err := sandbox.mutationPath(filepath.ToSlash(destinationRelative))
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destinationPath); err == nil {
		return ErrAlreadyExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapPathError(err)
	}
	if err := os.Rename(sourcePath, destinationPath); err != nil {
		return mapPathError(err)
	}
	return nil
}

func (s *Service) Delete(root, relativePath string, recursive bool) error {
	sandbox, err := newSandbox(root)
	if err != nil {
		return err
	}
	return sandbox.delete(relativePath, recursive)
}

// OpenDownload opens a validated regular file. The caller owns and must close
// the returned handle; no file content is accumulated in memory.
func (s *Service) OpenDownload(root, relativePath string) (*os.File, FileInfo, error) {
	sandbox, err := newSandbox(root)
	if err != nil {
		return nil, FileInfo{}, err
	}
	return sandbox.openDownload(relativePath)
}

// Upload streams a multipart file into a temporary file in the validated
// target directory and atomically commits it only after a complete transfer.
func (s *Service) Upload(root, targetDirectory, filename string, data io.Reader, overwrite bool) (FileInfo, error) {
	sandbox, err := newSandbox(root)
	if err != nil {
		return FileInfo{}, err
	}
	return sandbox.upload(targetDirectory, filename, data, overwrite, s.maxUploadBytes)
}

type sandbox struct{ root string }

func newSandbox(root string) (*sandbox, error) {
	if root == "" {
		return nil, ErrNotFound
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if unsafe, err := isReparsePoint(absRoot); err != nil {
		return nil, err
	} else if unsafe {
		return nil, ErrSpecialFile
	}
	_, err = filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, mapPathError(err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, mapPathError(err)
	}
	if !info.IsDir() {
		return nil, ErrExpectedDir
	}
	return &sandbox{root: filepath.Clean(absRoot)}, nil
}

func (s *sandbox) resolve(relativePath string) (string, error) {
	segments, err := cleanRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	if err := rejectReparseComponents(s.root, segments); err != nil {
		return "", err
	}
	target := s.root
	for _, segment := range segments {
		target = filepath.Join(target, segment)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", mapPathError(err)
	}
	if !within(s.root, resolved) {
		return "", ErrPathEscapesRoot
	}
	return resolved, nil
}

// mutationPath resolves the existing parent but intentionally does not resolve
// the final path. This makes create destinations possible and lets Linux
// delete or rename a symlink itself rather than acting on its target.
func (s *sandbox) mutationPath(relativePath string) (string, error) {
	segments, err := cleanRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	if len(segments) == 0 {
		return "", ErrRootOperation
	}
	parentSegments := segments[:len(segments)-1]
	if err := rejectReparseComponents(s.root, parentSegments); err != nil {
		return "", err
	}
	parent := s.root
	for _, segment := range parentSegments {
		parent = filepath.Join(parent, segment)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", mapPathError(err)
	}
	if !within(s.root, resolvedParent) {
		return "", ErrPathEscapesRoot
	}
	info, err := os.Stat(resolvedParent)
	if err != nil {
		return "", mapPathError(err)
	}
	if !info.IsDir() {
		return "", ErrExpectedDir
	}
	target := filepath.Join(resolvedParent, segments[len(segments)-1])
	if !within(s.root, target) {
		return "", ErrPathEscapesRoot
	}
	return target, nil
}

func cleanRelativePath(value string) ([]string, error) {
	if len(value) > MaxPathBytes || strings.IndexByte(value, 0) >= 0 {
		return nil, ErrInvalidPath
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if normalized == "" || normalized == "." {
		return nil, nil
	}
	if strings.HasPrefix(normalized, "/") || path.IsAbs(normalized) || isDrivePath(normalized) {
		return nil, ErrPathEscapesRoot
	}
	parts := strings.Split(normalized, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			return nil, ErrPathEscapesRoot
		default:
			segments = append(segments, part)
		}
	}
	return segments, nil
}

func isDrivePath(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func rejectReparseComponents(root string, segments []string) error {
	current := root
	for _, segment := range segments {
		current = filepath.Join(current, segment)
		unsafe, err := isReparsePoint(current)
		if err != nil {
			return mapPathError(err)
		}
		if unsafe {
			return ErrSpecialFile
		}
	}
	return nil
}

func within(root, target string) bool {
	if filepath.Separator == '\\' {
		root = strings.ToLower(filepath.Clean(root))
		target = strings.ToLower(filepath.Clean(target))
	}
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (s *sandbox) list(relativePath string) ([]Entry, error) {
	target, err := s.resolve(relativePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, mapPathError(err)
	}
	if !info.IsDir() {
		return nil, ErrExpectedDir
	}
	directory, err := os.Open(target)
	if err != nil {
		return nil, mapPathError(err)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(MaxDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, mapPathError(err)
	}
	if len(entries) > MaxDirectoryEntries {
		return nil, ErrTooLarge
	}
	base := relativeForAPI(relativePath)
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		childPath := entry.Name()
		if base != "" {
			childPath = base + "/" + entry.Name()
		}
		resolved, err := s.resolve(childPath)
		if err != nil {
			// Unsafe links and non-normal files are intentionally not exposed in
			// directory listings. A direct request receives the mapped error.
			continue
		}
		childInfo, err := os.Stat(resolved)
		if err != nil {
			continue
		}
		kind := ""
		switch {
		case childInfo.IsDir():
			kind = "directory"
		case childInfo.Mode().IsRegular():
			kind = "file"
		default:
			continue
		}
		result = append(result, Entry{
			Name:         entry.Name(),
			RelativePath: childPath,
			Type:         kind,
			Size:         childInfo.Size(),
			ModifiedAt:   childInfo.ModTime().UTC(),
			IsSymlink:    entry.Type()&fs.ModeSymlink != 0,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type == "directory"
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func (s *sandbox) stat(relativePath string) (FileInfo, error) {
	target, err := s.resolve(relativePath)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return FileInfo{}, mapPathError(err)
	}
	if !info.Mode().IsRegular() {
		if info.IsDir() {
			return FileInfo{}, ErrExpectedFile
		}
		return FileInfo{}, ErrSpecialFile
	}
	return FileInfo{RelativePath: relativeForAPI(relativePath), Size: info.Size(), ModifiedAt: info.ModTime().UTC()}, nil
}

func (s *sandbox) readFile(relativePath string) (Content, error) {
	fileInfo, err := s.stat(relativePath)
	if err != nil {
		return Content{}, err
	}
	if fileInfo.Size > MaxReadBytes {
		return Content{}, ErrTooLarge
	}
	target, err := s.resolve(relativePath)
	if err != nil {
		return Content{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return Content{}, mapPathError(err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxReadBytes+1))
	if err != nil {
		return Content{}, mapPathError(err)
	}
	if int64(len(data)) > MaxReadBytes {
		return Content{}, ErrTooLarge
	}
	if strings.IndexByte(string(data), 0) >= 0 || !utf8.Valid(data) {
		return Content{}, ErrBinaryFile
	}
	return Content{FileInfo: fileInfo, Encoding: "utf-8", Content: string(data)}, nil
}

func validateText(content string) error {
	if int64(len(content)) > MaxReadBytes {
		return ErrTooLarge
	}
	if strings.IndexByte(content, 0) >= 0 || !utf8.ValidString(content) {
		return ErrBinaryFile
	}
	return nil
}

func (s *sandbox) createFile(relativePath, content string) error {
	if err := validateText(content); err != nil {
		return err
	}
	target, err := s.mutationPath(relativePath)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrAlreadyExists
		}
		return mapPathError(err)
	}
	if _, err = io.WriteString(file, content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(target)
		return mapPathError(err)
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return mapPathError(closeErr)
	}
	return nil
}

func (s *sandbox) createDirectory(relativePath string) error {
	target, err := s.mutationPath(relativePath)
	if err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrAlreadyExists
		}
		return mapPathError(err)
	}
	return nil
}

func (s *sandbox) writeFile(relativePath, content string) error {
	if err := validateText(content); err != nil {
		return err
	}
	target, err := s.resolve(relativePath)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return mapPathError(err)
	}
	if info.IsDir() {
		return ErrExpectedFile
	}
	if !info.Mode().IsRegular() {
		return ErrSpecialFile
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".gamenode-write-*")
	if err != nil {
		return mapPathError(err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(info.Mode().Perm()); err == nil {
		_, err = io.WriteString(temporary, content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return mapPathError(err)
	}
	if err := atomicReplace(temporaryName, target); err != nil {
		return mapPathError(err)
	}
	return nil
}

func (s *sandbox) move(source, destination string) error {
	sourcePath, err := s.mutationPath(source)
	if err != nil {
		return err
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return mapPathError(err)
	}
	if unsafe, err := isReparsePoint(sourcePath); err != nil {
		return mapPathError(err)
	} else if unsafe {
		return ErrSpecialFile
	}
	if !sourceInfo.Mode().IsRegular() && !sourceInfo.IsDir() && sourceInfo.Mode()&fs.ModeSymlink == 0 {
		return ErrSpecialFile
	}
	destinationPath, err := s.mutationPath(destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destinationPath); err == nil {
		return ErrAlreadyExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapPathError(err)
	}
	if sourceInfo.IsDir() && within(sourcePath, destinationPath) {
		return ErrInvalidPath
	}
	if err := os.Rename(sourcePath, destinationPath); err != nil {
		return mapPathError(err)
	}
	return nil
}

func (s *sandbox) delete(relativePath string, recursive bool) error {
	target, err := s.mutationPath(relativePath)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return mapPathError(err)
	}
	if unsafe, err := isReparsePoint(target); err != nil {
		return mapPathError(err)
	} else if unsafe {
		return ErrSpecialFile
	}
	if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		return ErrSpecialFile
	}
	if info.IsDir() && recursive {
		if err := os.RemoveAll(target); err != nil {
			return mapPathError(err)
		}
		return nil
	}
	if info.IsDir() {
		entries, err := os.ReadDir(target)
		if err != nil {
			return mapPathError(err)
		}
		if len(entries) != 0 {
			return ErrDirectoryNotEmpty
		}
	}
	if err := os.Remove(target); err != nil {
		return mapPathError(err)
	}
	return nil
}

func (s *sandbox) openDownload(relativePath string) (*os.File, FileInfo, error) {
	target, err := s.resolve(relativePath)
	if err != nil {
		return nil, FileInfo{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, FileInfo{}, mapPathError(err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, FileInfo{}, mapPathError(err)
	}
	if info.IsDir() {
		file.Close()
		return nil, FileInfo{}, ErrExpectedFile
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, FileInfo{}, ErrSpecialFile
	}
	return file, FileInfo{RelativePath: relativeForAPI(relativePath), Size: info.Size(), ModifiedAt: info.ModTime().UTC()}, nil
}

func (s *sandbox) upload(targetDirectory, filename string, data io.Reader, overwrite bool, maxBytes int64) (FileInfo, error) {
	name, err := cleanUploadFilename(filename)
	if err != nil {
		return FileInfo{}, err
	}
	directory, err := s.resolve(targetDirectory)
	if err != nil {
		return FileInfo{}, err
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		return FileInfo{}, mapPathError(err)
	}
	if !directoryInfo.IsDir() {
		return FileInfo{}, ErrExpectedDir
	}
	target := filepath.Join(directory, name)
	if !within(s.root, target) {
		return FileInfo{}, ErrPathEscapesRoot
	}
	mode := os.FileMode(0o644)
	if info, err := os.Lstat(target); err == nil {
		if !overwrite {
			return FileInfo{}, ErrAlreadyExists
		}
		if unsafe, reparseErr := isReparsePoint(target); reparseErr != nil {
			return FileInfo{}, mapPathError(reparseErr)
		} else if unsafe || info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return FileInfo{}, ErrSpecialFile
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return FileInfo{}, mapPathError(err)
	}
	temporary, err := os.CreateTemp(directory, ".gamenode-upload-*")
	if err != nil {
		return FileInfo{}, mapPathError(err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(mode); err == nil {
		written, copyErr := io.Copy(temporary, io.LimitReader(data, maxBytes+1))
		if copyErr != nil {
			err = copyErr
		} else if written > maxBytes {
			err = ErrTooLarge
		}
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return FileInfo{}, ErrTooLarge
		}
		return FileInfo{}, mapPathError(err)
	}
	if err := commitUpload(temporaryName, target, overwrite); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return FileInfo{}, ErrAlreadyExists
		}
		return FileInfo{}, mapPathError(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		return FileInfo{}, mapPathError(err)
	}
	return FileInfo{RelativePath: joinRelative(targetDirectory, name), Size: info.Size(), ModifiedAt: info.ModTime().UTC()}, nil
}

func cleanUploadFilename(value string) (string, error) {
	if value == "" || len(value) > 255 || value == "." || value == ".." || strings.Contains(value, "..") || strings.ContainsAny(value, `\\/:<>"|?*`) {
		return "", ErrInvalidFilename
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", ErrInvalidFilename
		}
	}
	return value, nil
}

func joinRelative(directory, name string) string {
	base := relativeForAPI(directory)
	if base == "" {
		return name
	}
	return base + "/" + name
}

func relativeForAPI(value string) string {
	segments, err := cleanRelativePath(value)
	if err != nil {
		return ""
	}
	return strings.Join(segments, "/")
}

func mapPathError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if errors.Is(err, fs.ErrPermission) {
		return fs.ErrPermission
	}
	return fmt.Errorf("filesystem access: %w", err)
}
