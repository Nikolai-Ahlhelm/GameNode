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
	"sort"
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

// StartupConfiguration is the Minecraft Java process configuration exposed by
// the server dashboard. Arguments remain structured; heap flags are owned by
// XmsMB/XmxMB and are never accepted in JVMArguments.
type StartupConfiguration struct {
	Distribution     string   `json:"distribution"`
	JavaExecutable   string   `json:"java_executable"`
	XmsMB            int      `json:"xms_mb"`
	XmxMB            int      `json:"xmx_mb"`
	JVMArguments     []string `json:"jvm_arguments"`
	ProgramArguments []string `json:"program_arguments"`
}

// ResolveConfiguredLaunch resolves an already-installed Minecraft directory.
// NeoForge and Forge argument files are discovered under the install root so
// existing installations need not persist or hardcode a loader version.
func ResolveConfiguredLaunch(root, platform string, config StartupConfiguration, memoryLimitBytes int64, containerRuntime ...bool) (Launch, error) {
	inContainer := len(containerRuntime) > 0 && containerRuntime[0]
	if platform == "" {
		platform = runtime.GOOS
	}
	if platform != "windows" && platform != "linux" {
		return Launch{}, errors.New("Minecraft launch supports Windows and Linux")
	}
	if config.Distribution == "" {
		config.Distribution = "vanilla"
	}
	if config.Distribution != "vanilla" && config.Distribution != "neoforge" && config.Distribution != "forge" && config.Distribution != "fabric" && config.Distribution != "paper" {
		return Launch{}, errors.New("unsupported Minecraft distribution")
	}
	if config.XmsMB <= 0 || config.XmxMB <= 0 || config.XmsMB > config.XmxMB {
		return Launch{}, errors.New("Xms and Xmx must be positive and Xms must not exceed Xmx")
	}
	if memoryLimitBytes > 0 && int64(config.XmxMB)*1024*1024 > memoryLimitBytes {
		return Launch{}, errors.New("Xmx exceeds the container memory limit")
	}
	for _, args := range [][]string{config.JVMArguments, config.ProgramArguments} {
		if len(args) > 128 {
			return Launch{}, errors.New("too many Minecraft startup arguments")
		}
		for _, arg := range args {
			if len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") {
				return Launch{}, errors.New("invalid Minecraft startup argument")
			}
		}
	}
	for _, arg := range config.JVMArguments {
		if strings.HasPrefix(strings.ToLower(arg), "-xms") || strings.HasPrefix(strings.ToLower(arg), "-xmx") {
			return Launch{}, errors.New("heap arguments are managed by Xms and Xmx settings")
		}
	}
	root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
	if err != nil {
		return Launch{}, errors.New("invalid server root")
	}
	if info, e := os.Stat(root); e != nil || !info.IsDir() {
		return Launch{}, errors.New("server root must be an existing directory")
	}
	java := strings.TrimSpace(config.JavaExecutable)
	javaFound := false
	if java == "" {
		java, javaFound = DiscoverJava()
	}
	if java == "" || strings.ContainsRune(java, 0) {
		return Launch{}, errors.New("Java executable is required")
	}
	if inContainer {
		if strings.ContainsAny(java, "\x00\r\n") || strings.Contains(java, "..") {
			return Launch{}, errors.New("invalid container Java executable")
		}
		javaFound = false // The executable is resolved by the selected image at start.
	} else if !filepath.IsAbs(java) {
		found, e := exec.LookPath(java)
		if e != nil && java != "java" {
			return Launch{}, errors.New("Java executable was not found")
		}
		if e == nil {
			java, err = filepath.Abs(found)
			if err != nil {
				return Launch{}, errors.New("invalid Java executable")
			}
			javaFound = true
		}
	}
	if !inContainer && filepath.IsAbs(java) {
		if info, e := os.Stat(java); e != nil || info.IsDir() {
			return Launch{}, errors.New("Java executable must be an existing file")
		} else if runtime.GOOS == "linux" && info.Mode().Perm()&0o111 == 0 {
			return Launch{}, errors.New("Java executable is not executable")
		}
		javaFound = true
	}
	arguments := []string{fmt.Sprintf("-Xms%dM", config.XmsMB), fmt.Sprintf("-Xmx%dM", config.XmxMB)}
	arguments = append(arguments, config.JVMArguments...)
	switch config.Distribution {
	case "neoforge", "forge":
		argfile, e := discoverLoaderArgfile(root, config.Distribution, platform)
		if e != nil {
			return Launch{}, e
		}
		if data, e := readInRoot(root, "user_jvm_args.txt", maxArgfileBytes); e == nil {
			imported, importErr := ImportJVMArguments(data)
			if importErr != nil {
				return Launch{}, errors.New("user_jvm_args.txt could not be parsed safely")
			}
			seen := make(map[string]struct{}, len(config.JVMArguments))
			for _, argument := range config.JVMArguments {
				seen[argument] = struct{}{}
			}
			for _, argument := range imported.JVMArguments {
				if _, exists := seen[argument]; exists {
					continue
				}
				arguments = append(arguments, argument)
				seen[argument] = struct{}{}
			}
		}
		arguments = append(arguments, "@"+argfile)
	default:
		if _, _, err = regularFileInRoot(root, "server.jar"); err != nil {
			return Launch{}, errors.New("server.jar is missing")
		}
		arguments = append(arguments, "-jar", "server.jar")
	}
	arguments = append(arguments, config.ProgramArguments...)
	return Launch{Executable: java, Arguments: arguments, WorkingDirectory: root, JavaFound: javaFound, StopMethod: "stdin_command", StopCommand: "stop", StopTimeout: 60}, nil
}

func discoverLoaderArgfile(root, distribution, platform string) (string, error) {
	file := "unix_args.txt"
	if platform == "windows" {
		file = "win_args.txt"
	}
	base := "libraries/net/neoforged/neoforge"
	if distribution == "forge" {
		base = "libraries/net/minecraftforge/forge"
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(base)))
	if err != nil {
		return "", errors.New("loader launch argument file is missing")
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versionNewer(versions[i], versions[j]) })
	for _, version := range versions {
		relative := filepath.ToSlash(filepath.Join(base, version, file))
		data, e := readInRoot(root, relative, maxArgfileBytes)
		if e == nil && validateArgfile(data) == nil {
			return relative, nil
		}
	}
	return "", errors.New("no safe loader launch argument file was found")
}

func versionNewer(left, right string) bool {
	a, b := numericComponents.FindAllString(left, -1), numericComponents.FindAllString(right, -1)
	for i := 0; i < len(a) && i < len(b); i++ {
		av, ae := strconv.ParseUint(a[i], 10, 64)
		bv, be := strconv.ParseUint(b[i], 10, 64)
		if ae == nil && be == nil && av != bv {
			return av > bv
		}
		if (ae == nil) != (be == nil) {
			return ae == nil
		}
	}
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return left > right
}

// ImportJVMArguments extracts managed heap settings and retains every other
// parsed argument. Unsupported tokens are returned as an error, never dropped.
func ImportJVMArguments(data []byte) (StartupConfiguration, error) {
	if len(data) > maxArgfileBytes || bytes.ContainsRune(data, 0) {
		return StartupConfiguration{}, errors.New("JVM argument file is invalid or too large")
	}
	tokens, err := parseJVMArgumentText(string(data))
	if err != nil {
		return StartupConfiguration{}, err
	}
	config := StartupConfiguration{XmsMB: 1024, XmxMB: 4096}
	for _, token := range tokens {
		lower := strings.ToLower(token)
		if strings.HasPrefix(lower, "-xms") {
			value, e := parseHeapMB(token[4:])
			if e != nil {
				return StartupConfiguration{}, e
			}
			config.XmsMB = value
			continue
		}
		if strings.HasPrefix(lower, "-xmx") {
			value, e := parseHeapMB(token[4:])
			if e != nil {
				return StartupConfiguration{}, e
			}
			config.XmxMB = value
			continue
		}
		config.JVMArguments = append(config.JVMArguments, token)
	}
	if config.XmsMB > config.XmxMB {
		return StartupConfiguration{}, errors.New("imported Xms exceeds Xmx")
	}
	return config, nil
}

func parseHeapMB(value string) (int, error) {
	if value == "" {
		return 0, errors.New("invalid imported heap value")
	}
	unit := byte(0)
	if last := value[len(value)-1]; last < '0' || last > '9' {
		unit = last
		value = value[:len(value)-1]
	}
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid imported heap value")
	}
	switch unit {
	case 'g', 'G':
		n *= 1024
	case 'k', 'K':
		n = (n + 1023) / 1024
	case 'm', 'M', 0:
	default:
		return 0, errors.New("unsupported imported heap unit")
	}
	if n <= 0 || n > 1<<20 {
		return 0, errors.New("imported heap value is out of range")
	}
	return int(n), nil
}

func parseJVMArgumentText(text string) ([]string, error) {
	lines := strings.Split(text, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		filtered = append(filtered, line)
	}
	text = strings.Join(filtered, "\n")
	var out []string
	var token strings.Builder
	quote := rune(0)
	escaped := false
	active := false
	for _, r := range text {
		if escaped {
			token.WriteRune(r)
			escaped = false
			active = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			active = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			active = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			active = true
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if active {
				out = append(out, token.String())
				token.Reset()
				active = false
			}
			continue
		}
		token.WriteRune(r)
		active = true
	}
	if escaped || quote != 0 {
		return nil, errors.New("unclosed quote or escape in JVM argument file")
	}
	if active {
		out = append(out, token.String())
	}
	return out, nil
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
var numericComponents = regexp.MustCompile(`\d+`)

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
