package minecraft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const maxArgfileBytes = 64 << 10

// Launch is a fully resolved, structured Java launch. It is never a command
// string: Executable plus Arguments are persisted and started directly.
type Launch struct {
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"working_directory"`
	JavaFound        bool     `json:"java_found"`
	StopMethod       string   `json:"stop_method"`
	StopCommand      string   `json:"stop_command"`
	StopTimeout      int      `json:"stop_timeout_seconds"`
}

// ResolveLaunch derives the launch for an installed server from the plan alone.
// Forge and NeoForge read the installer-generated platform argument file at a
// path computed from the plan's versions; the generated run.sh/run.bat is never
// parsed or executed. Memory flags are typed values, not free-form JVM input.
func ResolveLaunch(root, platform string, plan Plan, minMemoryMB, maxMemoryMB int, nogui bool) (Launch, error) {
	if err := plan.Validate(); err != nil {
		return Launch{}, err
	}
	root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
	if err != nil {
		return Launch{}, errors.New("invalid server root")
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		return Launch{}, errors.New("server root must be an existing directory")
	}
	if platform == "" {
		platform = runtime.GOOS
	}
	if platform != "windows" && platform != "linux" {
		return Launch{}, errors.New("Minecraft launch supports Windows and Linux")
	}
	if minMemoryMB < 256 || maxMemoryMB < minMemoryMB {
		return Launch{}, errors.New("invalid Java memory range")
	}
	java, found := DiscoverJava()
	arguments := []string{fmt.Sprintf("-Xms%dM", minMemoryMB), fmt.Sprintf("-Xmx%dM", maxMemoryMB)}
	switch plan.Loader {
	case LoaderVanilla, LoaderFabric:
		if _, _, err = regularFileInRoot(root, ServerJarName); err != nil {
			return Launch{}, errors.New("server.jar is missing")
		}
		arguments = append(arguments, "-jar", ServerJarName)
	default:
		argfile := "unix_args.txt"
		if platform == "windows" {
			argfile = "win_args.txt"
		}
		relative := "libraries/net/neoforged/neoforge/" + plan.LoaderVersion + "/" + argfile
		if plan.Loader == LoaderForge {
			relative = "libraries/net/minecraftforge/forge/" + plan.MinecraftVersion + "-" + plan.LoaderVersion + "/" + argfile
		}
		data, readErr := readInRoot(root, relative, maxArgfileBytes)
		if readErr != nil {
			return Launch{}, errors.New("loader launch argument file is missing")
		}
		if err = validateArgfile(data); err != nil {
			return Launch{}, err
		}
		arguments = append(arguments, "@"+relative)
	}
	if nogui {
		arguments = append(arguments, "nogui")
	}
	return Launch{Executable: java, Arguments: arguments, WorkingDirectory: root, JavaFound: found, StopMethod: "stdin_command", StopCommand: "stop", StopTimeout: 60}, nil
}

// validateArgfile accepts a generated argument file only when it contains no
// nested argfile, JVM agent, absolute path, or traversal.
func validateArgfile(data []byte) error {
	if len(data) == 0 || bytes.ContainsRune(data, 0) {
		return errors.New("loader argument file is invalid")
	}
	for _, token := range strings.Fields(string(data)) {
		lower := strings.ToLower(strings.Trim(token, `"'`))
		normalized := strings.ReplaceAll(lower, "\\", "/")
		if strings.HasPrefix(lower, "@") || strings.HasPrefix(lower, "-javaagent") || strings.HasPrefix(lower, "-agentlib") || strings.HasPrefix(lower, "-agentpath") ||
			strings.HasPrefix(normalized, "/") || regexp.MustCompile(`^[a-z]:/`).MatchString(normalized) || normalized == ".." || strings.Contains(normalized, "/../") || strings.HasPrefix(normalized, "../") {
			return errors.New("loader argument file contains an unsafe path or JVM agent")
		}
	}
	return nil
}

// regularFileInRoot resolves relative below root, rejecting symlink escapes and
// non-regular files, and returns the resolved path and size.
func regularFileInRoot(root, relative string) (string, int64, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", 0, err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", 0, err
	}
	rel, err := filepath.Rel(canonicalRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", 0, errors.New("path escapes server root")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, errors.New("file unavailable")
	}
	return resolved, info.Size(), nil
}

func readInRoot(root, relative string, limit int64) ([]byte, error) {
	resolved, size, err := regularFileInRoot(root, relative)
	if err != nil {
		return nil, err
	}
	if size > limit {
		return nil, errors.New("file too large")
	}
	return os.ReadFile(resolved)
}

// WriteServerProperties creates server.properties with only the server port
// when no file exists. Minecraft fills every other default itself on first
// start; an existing file is never modified.
func WriteServerProperties(root string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("invalid server port")
	}
	file, err := os.OpenFile(filepath.Join(root, "server.properties"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	_, err = fmt.Fprintf(file, "server-port=%d\n", port)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// DiscoverJava resolves only JAVA_HOME/bin/java or the host PATH. Templates and
// users cannot select an arbitrary Java binary.
func DiscoverJava() (string, bool) {
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

var javaVersionPattern = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

// JavaMajor reports the installed Java's major version via a bounded
// `java -version` invocation, or 0 when it cannot be determined.
func JavaMajor() int {
	java, found := DiscoverJava()
	if !found {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, _ := exec.CommandContext(ctx, java, "-version").CombinedOutput()
	match := javaVersionPattern.FindSubmatch(output)
	if match == nil {
		return 0
	}
	major, _ := strconv.Atoi(string(match[1]))
	if major == 1 && len(match[2]) > 0 {
		major, _ = strconv.Atoi(string(match[2]))
	}
	return major
}

// AcceptEULA records acceptance of the Minecraft EULA by writing eula.txt. It
// must only be called after the operator explicitly opted in; GameNode never
// accepts the EULA on its own.
func AcceptEULA(root string) error {
	const content = "# By changing the setting below to TRUE you are indicating your agreement to our EULA (https://aka.ms/MinecraftEULA).\neula=true\n"
	temporary, err := os.CreateTemp(root, ".gamenode-eula-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err = temporary.WriteString(content); err == nil {
		err = temporary.Close()
	} else {
		_ = temporary.Close()
	}
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(root, "eula.txt"))
}
