package templates

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const maxLauncherBytes = 32 << 10

var neoForgeArgfile = regexp.MustCompile(`(?i)^@libraries/net/neoforged/neoforge/([^/]+)/((win|unix)_args\.txt)$`)

type NeoForgeResolution struct {
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"working_directory"`
	NeoForgeVersion  string   `json:"neoforge_version"`
	MinecraftVersion string   `json:"minecraft_version,omitempty"`
	Platform         string   `json:"platform"`
	JavaFound        bool     `json:"java_found"`
	StopMethod       string   `json:"stop_method"`
	StopCommand      string   `json:"stop_command"`
	StopTimeout      int      `json:"stop_timeout_seconds"`
}

// ResolveNeoForge reads a generated NeoForge launcher but never executes it.
// Only the exact direct Java + two local argfiles shape is accepted.
func ResolveNeoForge(root, platform string, minMemoryMB, maxMemoryMB int, nogui bool) (NeoForgeResolution, error) {
	root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
	if err != nil {
		return NeoForgeResolution{}, errors.New("invalid server root")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return NeoForgeResolution{}, errors.New("server root must be an existing directory")
	}
	if platform == "" {
		platform = runtime.GOOS
	}
	launcher := "run.sh"
	wanted := "unix_args.txt"
	if platform == "windows" {
		launcher, wanted = "run.bat", "win_args.txt"
	} else if platform != "linux" {
		return NeoForgeResolution{}, errors.New("NeoForge resolver supports Windows and Linux")
	}
	data, err := readLocal(root, launcher)
	if err != nil {
		return NeoForgeResolution{}, fmt.Errorf("NeoForge launcher %s is unavailable", launcher)
	}
	line, err := launcherJavaLine(string(data), platform)
	if err != nil {
		return NeoForgeResolution{}, err
	}
	parts, err := splitDirectArguments(line)
	if err != nil || len(parts) < 3 {
		return NeoForgeResolution{}, errors.New("NeoForge launcher is not a supported direct Java command")
	}
	javaBase := strings.ToLower(filepath.Base(parts[0]))
	if javaBase != "java" && javaBase != "java.exe" {
		return NeoForgeResolution{}, errors.New("NeoForge launcher does not invoke Java directly")
	}
	if parts[1] != "@user_jvm_args.txt" {
		return NeoForgeResolution{}, errors.New("NeoForge launcher does not use the expected local JVM argument file")
	}
	match := neoForgeArgfile.FindStringSubmatch(strings.ReplaceAll(parts[2], "\\", "/"))
	if len(match) == 0 || !strings.EqualFold(match[2], wanted) {
		return NeoForgeResolution{}, errors.New("NeoForge launcher has an unexpected platform argument file")
	}
	for _, argument := range parts[3:] {
		if argument != "%*" && argument != "$@" && argument != `"$@"` {
			return NeoForgeResolution{}, errors.New("NeoForge launcher contains unexpected arguments")
		}
	}
	for _, relative := range []string{"user_jvm_args.txt", strings.TrimPrefix(parts[2], "@")} {
		if _, err = readLocal(root, relative); err != nil {
			return NeoForgeResolution{}, errors.New("NeoForge launcher references a missing or unsafe argument file")
		}
	}
	argData, _ := readLocal(root, strings.TrimPrefix(parts[2], "@"))
	if err = validateNeoForgeArgfile(argData); err != nil {
		return NeoForgeResolution{}, err
	}
	if minMemoryMB < 256 || maxMemoryMB < minMemoryMB {
		return NeoForgeResolution{}, errors.New("invalid Java memory range")
	}
	java, found := discoverJava()
	// The generated user_jvm_args.txt is verified as launcher metadata but is
	// deliberately not passed to Java. Built-in typed memory controls replace
	// the reference file's empty/comment-only defaults without exposing a
	// free-form JVM/agent/classpath injection surface.
	arguments := []string{fmt.Sprintf("-Xms%dM", minMemoryMB), fmt.Sprintf("-Xmx%dM", maxMemoryMB), parts[2]}
	if nogui {
		arguments = append(arguments, "nogui")
	}
	result := NeoForgeResolution{Executable: java, Arguments: arguments, WorkingDirectory: root, NeoForgeVersion: match[1], Platform: platform, JavaFound: found, StopMethod: "stdin_command", StopCommand: "stop", StopTimeout: 60}
	for i, token := range strings.Fields(string(argData)) {
		if token == "--fml.mcVersion" {
			fields := strings.Fields(string(argData))
			if i+1 < len(fields) {
				result.MinecraftVersion = fields[i+1]
			}
			break
		}
	}
	return result, nil
}

func validateNeoForgeArgfile(data []byte) error {
	if len(data) == 0 || len(data) > maxLauncherBytes || strings.ContainsRune(string(data), 0) {
		return errors.New("NeoForge platform argument file is invalid")
	}
	tokens, err := splitDirectArguments(string(data))
	if err != nil {
		return errors.New("NeoForge platform argument file cannot be safely parsed")
	}
	mainFound, versionFound := false, false
	for _, token := range tokens {
		lower := strings.ToLower(token)
		normalized := strings.ReplaceAll(token, "\\", "/")
		if token == "net.neoforged.fml.startup.Server" {
			mainFound = true
		}
		if token == "--fml.neoForgeVersion" {
			versionFound = true
		}
		if strings.HasPrefix(token, "@") || strings.HasPrefix(lower, "-javaagent") || strings.HasPrefix(lower, "-agentlib") || strings.HasPrefix(lower, "-agentpath") || absoluteExecutable(token) || normalized == ".." || strings.Contains(normalized, "/../") || strings.HasPrefix(normalized, "../") {
			return errors.New("NeoForge platform argument file contains an unsafe path or JVM agent")
		}
	}
	if !mainFound || !versionFound {
		return errors.New("NeoForge platform argument file lacks expected launch metadata")
	}
	return nil
}

func launcherJavaLine(data, platform string) (string, error) {
	if len(data) > maxLauncherBytes || strings.ContainsRune(data, 0) {
		return "", errors.New("NeoForge launcher is too large or invalid")
	}
	var command string
	for _, raw := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		lower := strings.ToLower(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(lower, "rem ") || lower == "@echo off" || lower == "pause" || strings.HasPrefix(line, "#!") {
			continue
		}
		if platform == "linux" && strings.HasPrefix(line, "exec ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "exec "))
		}
		if command != "" || strings.ContainsAny(line, "|;&><`") || strings.Contains(line, "$(") {
			return "", errors.New("NeoForge launcher contains unsupported shell or extra command semantics")
		}
		command = line
	}
	if command == "" {
		return "", errors.New("NeoForge launcher has no direct Java command")
	}
	return command, nil
}

func readLocal(root, relative string) ([]byte, error) {
	relative = filepath.FromSlash(strings.TrimSpace(relative))
	if filepath.IsAbs(relative) {
		return nil, errors.New("absolute path rejected")
	}
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("path escapes server root")
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > maxLauncherBytes {
		return nil, errors.New("local file unavailable")
	}
	return os.ReadFile(path)
}

func discoverJava() (string, bool) {
	name := "java"
	if runtime.GOOS == "windows" {
		name = "java.exe"
	}
	if home := strings.TrimSpace(os.Getenv("JAVA_HOME")); home != "" {
		candidate := filepath.Join(home, "bin", name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	if candidate, err := exec.LookPath(name); err == nil {
		if absolute, absErr := filepath.Abs(candidate); absErr == nil {
			return absolute, true
		}
		return candidate, true
	}
	return name, false
}
