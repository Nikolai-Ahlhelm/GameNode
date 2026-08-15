package templates

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ResolvedLaunch is the only template output consumed by provisioning. Every
// path has been expanded, canonicalized, and checked against ServerRoot; args
// remain an argv slice and are never joined into a command string.
type ResolvedLaunch struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	Environment      map[string]string
	StopMethod       string
	StopCommand      string
	StopTimeout      int
}

func ResolveLaunch(template Template, platform string, values map[string]string, serverRoot string) (ResolvedLaunch, error) {
	launch, ok := LaunchForPlatform(template, platform)
	if !ok {
		return ResolvedLaunch{}, validationError(CodeInvalidPlatformLaunch, "platform launch definition is missing")
	}
	if err := CheckHostRequirements(template, platform, ""); err != nil {
		return ResolvedLaunch{}, err
	}
	definitions := make(map[string]TemplateVariable, len(template.Variables))
	for _, variable := range template.Variables {
		definitions[variable.Key] = variable
	}
	if err := validateLaunchSensitive(*launch, definitions); err != nil {
		return ResolvedLaunch{}, err
	}
	if launch.Resolver == "neoforge" {
		minimum, minErr := strconv.Atoi(values["MIN_MEMORY_MB"])
		maximum, maxErr := strconv.Atoi(values["MAX_MEMORY_MB"])
		nogui := values["NOGUI"] == "1" || strings.EqualFold(values["NOGUI"], "true")
		if minErr != nil || maxErr != nil {
			return ResolvedLaunch{}, validationError(CodeInvalidVariable, "NeoForge memory variables are invalid")
		}
		resolved, err := ResolveNeoForge(serverRoot, platform, minimum, maximum, nogui)
		if err != nil {
			return ResolvedLaunch{}, validationError(CodeInvalidPlatformLaunch, "NeoForge launch files could not be resolved safely")
		}
		if !resolved.JavaFound {
			return ResolvedLaunch{}, validationError(CodeRequirementUnavailable, "Java runtime not found")
		}
		if err = ValidateExpectedFiles(template, platform, values, serverRoot); err != nil {
			return ResolvedLaunch{}, err
		}
		return ResolvedLaunch{Executable: resolved.Executable, Arguments: resolved.Arguments, WorkingDirectory: resolved.WorkingDirectory, Environment: map[string]string{}, StopMethod: resolved.StopMethod, StopCommand: resolved.StopCommand, StopTimeout: resolved.StopTimeout}, nil
	}

	known := make(map[string]bool, len(values))
	for key := range values {
		known[key] = true
	}
	workingDirectory := serverRoot
	if launch.WorkingDirectory != "" {
		relative, err := ExpandRelativePath(launch.WorkingDirectory, values, known)
		if err != nil {
			return ResolvedLaunch{}, validationError(CodeInvalidPath, "working directory expansion failed")
		}
		workingDirectory, err = resolveExistingPath(serverRoot, relative, true)
		if err != nil {
			return ResolvedLaunch{}, validationError(CodeInvalidPath, "working directory is missing or unsafe")
		}
	}

	executable := ""
	if launch.Resolver == "java" {
		var found bool
		executable, found = DiscoverJava()
		if !found {
			return ResolvedLaunch{}, validationError(CodeRequirementUnavailable, "Java runtime not found")
		}
	} else if launch.Resolver != "" {
		return ResolvedLaunch{}, validationError(CodeInvalidPlatformLaunch, "launch resolver is unsupported")
	} else {
		relative, err := ExpandRelativePath(launch.Executable, values, known)
		if err != nil {
			return ResolvedLaunch{}, validationError(CodeInvalidPath, "launch executable expansion failed")
		}
		executable, err = resolveExistingPath(serverRoot, relative, false)
		if err != nil {
			return ResolvedLaunch{}, validationError(CodeInvalidPath, "launch executable is missing or unsafe")
		}
	}

	arguments := make([]string, 0, len(launch.Arguments))
	for _, raw := range launch.Arguments {
		value, err := Expand(raw, values, known)
		if err != nil {
			return ResolvedLaunch{}, validationError(CodeInvalidPlatformLaunch, "launch argument expansion failed")
		}
		arguments = append(arguments, value)
	}
	environment := make(map[string]string, len(launch.Environment))
	for key, raw := range launch.Environment {
		value, err := Expand(raw, values, known)
		if err != nil {
			return ResolvedLaunch{}, validationError(CodeInvalidPlatformLaunch, "launch environment expansion failed")
		}
		environment[key] = value
	}
	if err := ValidateExpectedFiles(template, platform, values, serverRoot); err != nil {
		return ResolvedLaunch{}, err
	}
	stopMethod, stopTimeout := launch.StopMethod, launch.StopTimeout
	if stopMethod == "" {
		stopMethod = "terminate"
	}
	if stopTimeout == 0 {
		stopTimeout = 15
	}
	return ResolvedLaunch{Executable: executable, Arguments: arguments, WorkingDirectory: workingDirectory, Environment: environment, StopMethod: stopMethod, StopCommand: launch.StopCommand, StopTimeout: stopTimeout}, nil
}

// ValidateExpectedFiles checks required artifacts after installation. Symlinks
// are resolved and must remain below serverRoot; optional missing artifacts are
// ignored, while present optional artifacts are still sandbox-checked.
func ValidateExpectedFiles(template Template, platform string, values map[string]string, serverRoot string) error {
	known := make(map[string]bool, len(values))
	for key := range values {
		known[key] = true
	}
	for _, expected := range template.ExpectedFiles {
		if expected.Platform != "" && expected.Platform != platform {
			continue
		}
		relative, err := ExpandRelativePath(expected.Path, values, known)
		if err != nil {
			return validationError(CodeExpectedFileInvalid, "expected file path expansion failed")
		}
		candidate := filepath.Join(serverRoot, filepath.FromSlash(relative))
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			if !expected.Required && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return validationError(CodeExpectedFileInvalid, "required installation artifact is missing")
		}
		if !withinRoot(serverRoot, resolved) {
			return validationError(CodeExpectedFileInvalid, "installation artifact escapes the server root")
		}
		info, err := os.Stat(resolved)
		if err != nil || (expected.Type == "file" && !info.Mode().IsRegular()) || (expected.Type == "directory" && !info.IsDir()) {
			return validationError(CodeExpectedFileInvalid, "installation artifact has the wrong type")
		}
		if expected.Executable && platform != "windows" && info.Mode().Perm()&0o111 == 0 {
			return validationError(CodeExpectedFileInvalid, "installation artifact is not executable")
		}
	}
	return nil
}

func resolveExistingPath(root, relative string, directory bool) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || !withinRoot(root, resolved) {
		return "", errors.New("path is missing or unsafe")
	}
	info, err := os.Stat(resolved)
	if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return "", errors.New("path has wrong type")
	}
	return resolved, nil
}

func withinRoot(root, candidate string) bool {
	absoluteRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return false
	}
	absoluteCandidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteRoot, absoluteCandidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
