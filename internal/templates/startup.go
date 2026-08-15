package templates

import (
	"errors"
	"strings"
	"unicode"
)

var forbiddenExecutables = map[string]bool{"sh": true, "sh.exe": true, "bash": true, "bash.exe": true, "cmd": true, "cmd.exe": true, "powershell": true, "powershell.exe": true, "pwsh": true, "pwsh.exe": true, "eval": true, "exec": true, "source": true, ".": true, "for": true, "while": true, "until": true, "if": true, "case": true, "do": true}

func analyzeStartup(startup string, known map[string]bool) (*LaunchDefinition, []Finding) {
	startup = strings.TrimSpace(startup)
	if startup == "" {
		return nil, []Finding{{SeverityError, "startup", "STARTUP_MISSING", "Egg startup command is missing"}}
	}
	prefix, operator, err := safeProcessPrefix(startup)
	if err != nil {
		return nil, []Finding{{SeverityError, "startup", "UNSUPPORTED_SHELL_STARTUP", err.Error()}}
	}
	parts, err := splitDirectArguments(prefix)
	if err != nil || len(parts) == 0 {
		return nil, []Finding{{SeverityError, "startup", "STARTUP_PARSE_FAILED", "Startup cannot be represented as executable plus arguments"}}
	}
	mapped := false
	for index, part := range parts {
		normalized, changed := mapContainerPath(part)
		parts[index], mapped = normalized, mapped || changed
	}
	executable := strings.ToLower(strings.TrimSpace(parts[0]))
	base := executable
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	if forbiddenExecutables[base] {
		return nil, []Finding{{SeverityError, "startup", "UNSUPPORTED_SHELL_STARTUP", "Startup invokes a shell or shell builtin"}}
	}
	if absoluteExecutable(parts[0]) {
		return nil, []Finding{{SeverityError, "startup", "ABSOLUTE_EXECUTABLE_REJECTED", "Absolute Egg executable paths are never mapped to host paths"}}
	}
	for _, part := range parts {
		if unsafeLaunchPath(part) {
			return nil, []Finding{{SeverityError, "startup", "UNSAFE_LAUNCH_PATH", "Startup contains an absolute or traversal-like host path"}}
		}
	}
	findings := []Finding{}
	if mapped {
		findings = append(findings, Finding{SeverityInfo, "startup", "CONTAINER_PATH_MAPPED", "Container server paths were mapped to the semantic GameNode server root"})
	}
	if operator != "" {
		findings = append(findings, Finding{SeverityWarning, "startup", "UNSUPPORTED_SHELL_STARTUP", "Only the direct process before shell operator " + operator + " was imported; the shell tail is ignored"})
	}
	for _, part := range parts {
		if strings.Contains(part, "$(") || strings.Contains(part, "`") {
			return nil, []Finding{{SeverityError, "startup", "COMMAND_SUBSTITUTION_REJECTED", "Command substitution is not supported"}}
		}
		matches := placeholder.FindAllStringSubmatch(part, -1)
		for _, match := range matches {
			key := match[1]
			if key == "" {
				key = match[2]
			}
			if !known[key] {
				findings = append(findings, Finding{SeverityWarning, "startup", "UNKNOWN_STARTUP_VARIABLE", "Startup references unknown variable " + key})
			}
		}
	}
	return &LaunchDefinition{Executable: parts[0], Arguments: parts[1:], WorkingRoot: "server_root"}, findings
}

func mapContainerPath(value string) (string, bool) {
	result := value
	for _, root := range []string{"/home/container", "/mnt/server"} {
		result = strings.ReplaceAll(result, root, ".")
	}
	return result, result != value
}

func safeProcessPrefix(input string) (string, string, error) {
	quote := rune(0)
	escaped := false
	for i, r := range input {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == '`' {
			return "", "", errors.New("backtick command substitution is not supported")
		}
		if r == '$' && i+1 < len(input) && input[i+1] == '(' {
			return "", "", errors.New("command substitution is not supported")
		}
		switch r {
		case '&', '|', ';', '>', '<':
			op := string(r)
			if i+1 < len(input) && rune(input[i+1]) == r {
				op += string(r)
			}
			prefix := strings.TrimSpace(input[:i])
			if prefix == "" {
				return "", "", errors.New("startup begins with a shell operator")
			}
			return prefix, op, nil
		}
	}
	if quote != 0 || escaped {
		return "", "", errors.New("unterminated startup quoting")
	}
	return input, "", nil
}

func splitDirectArguments(input string) ([]string, error) {
	var result []string
	var current strings.Builder
	quote := rune(0)
	escaped := false
	token := false
	flush := func() {
		if token {
			result = append(result, current.String())
			current.Reset()
			token = false
		}
	}
	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			token = true
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			token = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			token = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			token = true
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		current.WriteRune(r)
		token = true
	}
	if quote != 0 || escaped {
		return nil, errors.New("unterminated quoting")
	}
	flush()
	return result, nil
}

func safeStopCommand(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n;&|><`") && !strings.Contains(value, "$(")
}

func absoluteExecutable(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\\\") || (len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/'))
}

func unsafeLaunchPath(value string) bool {
	normalized := strings.ReplaceAll(value, "\\", "/")
	if normalized == ".." || strings.HasPrefix(normalized, "../") || strings.Contains(normalized, "/../") {
		return true
	}
	candidate := normalized
	if index := strings.Index(candidate, "="); index >= 0 {
		candidate = candidate[index+1:]
	}
	return absoluteExecutable(candidate)
}
