package vintagestory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Launch is a fully resolved, structured launch. It is never a command string.
type Launch struct {
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments"`
	WorkingDirectory string   `json:"working_directory"`
	DotnetFound      bool     `json:"dotnet_found"`
	StopMethod       string   `json:"stop_method"`
	StopCommand      string   `json:"stop_command"`
	StopTimeout      int      `json:"stop_timeout_seconds"`
	// ConsoleLineEnding is "crlf": the server console only treats CR+LF as Enter,
	// so a bare LF would leave the /stop command unexecuted.
	ConsoleLineEnding string `json:"console_line_ending"`
}

// ResolveLaunch derives the launch of an installed server: the host's dotnet
// executable running the server assembly with the data path and port. Paths in
// the arguments are server-root-relative (the working directory is the root),
// so a tenant migration that moves the root keeps the launch valid.
func ResolveLaunch(root string, port int) (Launch, error) {
	root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
	if err != nil {
		return Launch{}, errors.New("invalid server root")
	}
	if info, statErr := os.Stat(filepath.Join(root, ServerDLL)); statErr != nil || !info.Mode().IsRegular() {
		return Launch{}, errors.New("server assembly is missing")
	}
	if port < 1 || port > 65535 {
		return Launch{}, errors.New("invalid server port")
	}
	dotnet, found := DiscoverDotnet()
	return Launch{
		Executable:        dotnet,
		Arguments:         []string{ServerDLL, "--dataPath", DataDirectory, "--port", strconv.Itoa(port)},
		WorkingDirectory:  root,
		DotnetFound:       found,
		StopMethod:        "stdin_command",
		StopCommand:       "/stop",
		StopTimeout:       60,
		ConsoleLineEnding: "crlf",
	}, nil
}

// DiscoverDotnet resolves only DOTNET_ROOT/dotnet or the host PATH. Templates
// and users cannot select an arbitrary executable.
func DiscoverDotnet() (string, bool) {
	name := "dotnet"
	if runtime.GOOS == "windows" {
		name = "dotnet.exe"
	}
	if home := strings.TrimSpace(os.Getenv("DOTNET_ROOT")); home != "" {
		candidate := filepath.Join(home, name)
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

var runtimeLine = regexp.MustCompile(`^Microsoft\.NETCore\.App (\d+)\.`)

// InstalledRuntimeMajors lists the Microsoft.NETCore.App major versions the
// host dotnet reports, via a bounded `dotnet --list-runtimes`.
func InstalledRuntimeMajors() map[int]bool {
	result := map[int]bool{}
	dotnet, found := DiscoverDotnet()
	if !found {
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, _ := exec.CommandContext(ctx, dotnet, "--list-runtimes").Output()
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r", ""), "\n") {
		if match := runtimeLine.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			major, _ := strconv.Atoi(match[1])
			result[major] = true
		}
	}
	return result
}
