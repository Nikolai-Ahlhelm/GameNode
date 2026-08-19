package templates

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	environmentKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	sensitiveKey   = regexp.MustCompile(`(?i)(PASSWORD|PASS($|_)|TOKEN|SECRET|API_?KEY|AUTH)`)
	containerImage = regexp.MustCompile(`^(?:(?:[a-z0-9](?:[a-z0-9.-]{0,62}[a-z0-9])?)(?::[0-9]{1,5})?/)?(?:[a-z0-9][a-z0-9._-]{0,127}/)*[a-z0-9][a-z0-9._-]{0,127}(?::[A-Za-z0-9][A-Za-z0-9_.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)
)

type eggDocument struct {
	Meta         eggMeta                    `json:"meta"`
	ExportedAt   string                     `json:"exported_at"`
	Name         string                     `json:"name"`
	Author       string                     `json:"author"`
	UUID         string                     `json:"uuid"`
	Description  string                     `json:"description"`
	Tags         []string                   `json:"tags"`
	Features     []string                   `json:"features"`
	DockerImages map[string]string          `json:"docker_images"`
	FileDenylist []string                   `json:"file_denylist"`
	Startup      string                     `json:"startup"`
	Config       map[string]json.RawMessage `json:"config"`
	Scripts      eggScripts                 `json:"scripts"`
	Variables    []eggVariable              `json:"variables"`
}
type eggMeta struct {
	Version string `json:"version"`
}
type eggScripts struct {
	Installation eggInstallation `json:"installation"`
}
type eggInstallation struct{ Script, Container, Entrypoint string }
type eggVariable struct {
	Name, Description, EnvVariable, DefaultValue, Rules, FieldType string
	UserViewable, UserEditable                                     bool
}

func (v *eggVariable) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		EnvVariable  string `json:"env_variable"`
		DefaultValue any    `json:"default_value"`
		UserViewable bool   `json:"user_viewable"`
		UserEditable bool   `json:"user_editable"`
		Rules        string `json:"rules"`
		FieldType    string `json:"field_type"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	v.Name, v.Description, v.EnvVariable, v.Rules, v.FieldType = raw.Name, raw.Description, raw.EnvVariable, raw.Rules, raw.FieldType
	v.UserViewable, v.UserEditable = raw.UserViewable, raw.UserEditable
	switch value := raw.DefaultValue.(type) {
	case nil:
		v.DefaultValue = ""
	case string:
		v.DefaultValue = value
	case float64:
		v.DefaultValue = strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		v.DefaultValue = strconv.FormatBool(value)
	default:
		return errors.New("variable default_value must be scalar")
	}
	return nil
}

func AnalyzeEgg(data []byte) (Template, error) {
	if len(data) == 0 {
		return Template{}, errors.New("egg JSON is required")
	}
	if len(data) > MaxEggBytes {
		return Template{}, errors.New("egg JSON exceeds 256 KiB limit")
	}
	if err := checkJSONDepth(data, MaxNestingDepth); err != nil {
		return Template{}, err
	}
	var top map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&top); err != nil {
		return Template{}, errors.New("invalid egg JSON")
	}
	if decoder.Decode(&struct{}{}) == nil {
		return Template{}, errors.New("egg input must contain one JSON document")
	}
	var egg eggDocument
	if err := json.Unmarshal(data, &egg); err != nil {
		return Template{}, errors.New("invalid egg structure")
	}
	if strings.TrimSpace(egg.Name) == "" {
		return Template{}, errors.New("egg name is required")
	}
	if len(egg.Variables) > MaxVariables {
		return Template{}, fmt.Errorf("egg contains more than %d variables", MaxVariables)
	}
	if err := validateEggStrings(egg); err != nil {
		return Template{}, err
	}

	sum := sha256.Sum256(data)
	result := Template{Name: strings.TrimSpace(egg.Name), Description: strings.TrimSpace(egg.Description), SourceType: SourcePelicanPterodactyl, SourceIdentifier: strings.TrimSpace(egg.UUID), SourceFormatVersion: strings.TrimSpace(egg.Meta.Version)}
	result.SourceMetadata = SourceMetadata{Author: strings.TrimSpace(egg.Author), ExportedAt: egg.ExportedAt, Tags: boundedStrings(egg.Tags), Features: boundedStrings(egg.Features), DockerImages: egg.DockerImages, OriginalHash: hex.EncodeToString(sum[:])}
	knownFields := map[string]bool{"_comment": true, "meta": true, "exported_at": true, "name": true, "author": true, "uuid": true, "description": true, "tags": true, "features": true, "docker_images": true, "file_denylist": true, "startup": true, "config": true, "scripts": true, "variables": true}
	for key := range top {
		if !knownFields[key] {
			result.SourceMetadata.IgnoredFields = append(result.SourceMetadata.IgnoredFields, key)
		}
	}
	sort.Strings(result.SourceMetadata.IgnoredFields)
	for _, key := range result.SourceMetadata.IgnoredFields {
		addFinding(&result, SeverityInfo, "source", "UNKNOWN_EGG_FIELD", "Ignored unknown top-level field: "+key)
	}
	analyzeNestedStructures(top, &result)
	if len(egg.FileDenylist) > 0 {
		addFinding(&result, SeverityWarning, "config", "FILE_DENYLIST_UNSUPPORTED", "Egg file denylist rules are not enforced by the native GameNode filesystem")
	}

	knownVariables := map[string]bool{}
	seen := map[string]bool{}
	for _, variable := range egg.Variables {
		key := strings.TrimSpace(variable.EnvVariable)
		folded := strings.ToUpper(key)
		if !environmentKey.MatchString(key) {
			return Template{}, fmt.Errorf("invalid egg variable key %q", key)
		}
		if seen[folded] {
			return Template{}, fmt.Errorf("duplicate egg variable key %q", key)
		}
		seen[folded], knownVariables[key] = true, true
		parsed, warnings, err := parseVariable(variable)
		if err != nil {
			return Template{}, fmt.Errorf("variable %s: %w", key, err)
		}
		result.Variables = append(result.Variables, parsed)
		if parsed.Sensitive && variable.DefaultValue != "" {
			addFinding(&result, SeverityWarning, "variables", "SENSITIVE_DEFAULT_REMOVED", key+": a sensitive source default was discarded")
		}
		for _, warning := range warnings {
			addFinding(&result, SeverityWarning, "variables", "UNKNOWN_VALIDATION_RULE", key+": "+warning)
		}
	}
	for _, builtin := range []struct {
		key, name string
		min, max  float64
	}{{"SERVER_PORT", "Server Port", 1, 65535}, {"SERVER_MEMORY", "Server Memory", 1, 0}} {
		if startupReferences(egg.Startup, builtin.key) && !knownVariables[builtin.key] {
			if len(result.Variables) >= MaxVariables {
				return Template{}, fmt.Errorf("egg requires more than %d variables after native built-ins", MaxVariables)
			}
			variable := TemplateVariable{Name: builtin.name, Description: "GameNode runtime value referenced by the Egg startup", Key: builtin.key, UserViewable: true, UserEditable: true, Type: "integer", Required: true}
			variable.Validation.Min = &builtin.min
			if builtin.max > 0 {
				variable.Validation.Max = &builtin.max
			}
			result.Variables = append(result.Variables, variable)
			knownVariables[builtin.key] = true
			addFinding(&result, SeverityInfo, "variables", "IMPLICIT_RUNTIME_VARIABLE", builtin.key+" was added from a recognized Egg runtime placeholder")
		}
	}

	result.Installer = detectInstaller(egg, result.Variables, &result)
	if len(egg.DockerImages) > 0 || egg.Scripts.Installation.Container != "" {
		addFinding(&result, SeverityInfo, "runtime", "CONTAINER_METADATA_IGNORED", "Container images and paths are metadata only; GameNode uses its native runtime")
	}
	if strings.TrimSpace(egg.Scripts.Installation.Script) != "" {
		addFinding(&result, SeverityWarning, "installer", "UNSUPPORTED_INSTALL_SCRIPT", "The untrusted Egg installation script will not be executed")
	}
	launch, launchFindings := analyzeStartup(egg.Startup, knownVariables)
	result.Launch = launch
	for _, finding := range launchFindings {
		addFinding(&result, finding.Severity, finding.Component, finding.Code, finding.Summary)
	}
	if stop, ok := egg.Config["stop"]; ok {
		var command string
		if json.Unmarshal(stop, &command) == nil && result.Launch != nil && safeStopCommand(command) {
			result.Launch.StopCommand = command
		}
	}
	for key := range egg.Config {
		if key != "files" && key != "startup" && key != "logs" && key != "stop" {
			addFinding(&result, SeverityWarning, "config", "UNKNOWN_CONFIG_STRUCTURE", "Unsupported config structure: "+key)
		}
	}
	for _, key := range []string{"files", "startup", "logs"} {
		if raw, ok := egg.Config[key]; ok && meaningfulConfig(raw) {
			addFinding(&result, SeverityWarning, "config", "CONFIG_PARSER_UNSUPPORTED", "Egg config "+key+" rules are retained only as a compatibility signal")
		}
	}
	result.ContainerRuntime, result.ContainerCompatibility = analyzeContainerRuntime(egg, result.Variables, knownVariables)
	finalizeCompatibility(&result)
	result.NativeCompatibility = result.Compatibility
	return result, nil
}

func analyzeNestedStructures(top map[string]json.RawMessage, result *Template) {
	var scripts map[string]json.RawMessage
	if json.Unmarshal(top["scripts"], &scripts) != nil {
		return
	}
	for key, raw := range scripts {
		if key != "installation" {
			addFinding(result, SeverityWarning, "installer", "UNKNOWN_SCRIPT_STRUCTURE", "Unsupported Egg script structure: "+key)
			continue
		}
		var installation map[string]json.RawMessage
		if json.Unmarshal(raw, &installation) != nil {
			continue
		}
		for field := range installation {
			if field != "script" && field != "container" && field != "entrypoint" {
				addFinding(result, SeverityWarning, "installer", "UNKNOWN_INSTALL_STRUCTURE", "Unsupported installation field: "+field)
			}
		}
	}
}

func meaningfulConfig(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		trimmed := strings.TrimSpace(value)
		return trimmed != "" && trimmed != "{}" && trimmed != "[]"
	}
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null" && trimmed != "{}" && trimmed != "[]"
}

func validateEggStrings(egg eggDocument) error {
	if len(egg.Tags) > 64 || len(egg.Features) > 64 || len(egg.DockerImages) > 64 || len(egg.FileDenylist) > 64 || len(egg.Config) > 32 {
		return errors.New("egg collection limit exceeded")
	}
	if len(egg.Scripts.Installation.Script) > MaxContainerScriptBytes {
		return errors.New("egg installation script exceeds the bounded container limit")
	}
	values := []string{egg.Name, egg.Description, egg.Author, egg.UUID, egg.ExportedAt, egg.Meta.Version, egg.Startup, egg.Scripts.Installation.Script, egg.Scripts.Installation.Container, egg.Scripts.Installation.Entrypoint}
	for key, value := range egg.DockerImages {
		if len(key) > 1024 || len(value) > 1024 {
			return errors.New("egg container metadata is too long")
		}
	}
	for _, variable := range egg.Variables {
		values = append(values, variable.Name, variable.Description, variable.EnvVariable, variable.DefaultValue, variable.Rules, variable.FieldType)
	}
	for _, value := range values {
		if len(value) > MaxStringBytes || strings.ContainsRune(value, 0) {
			return errors.New("egg contains an invalid or excessively long string")
		}
	}
	return nil
}

// ValidateContainerImageReference accepts only a bounded Docker-style image
// reference. Schemes, paths to local files, whitespace, and shell syntax are
// rejected before an image can reach policy or the Engine API.
func ValidateContainerImageReference(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n \t") || strings.Contains(value, "://") || !containerImage.MatchString(value) {
		return errors.New("invalid container image reference")
	}
	return nil
}

func analyzeContainerRuntime(egg eggDocument, variables []TemplateVariable, known map[string]bool) (*ContainerEggRuntimePlan, Compatibility) {
	compatibility := Compatibility{Status: Compatible}
	plan := &ContainerEggRuntimePlan{StartupTemplate: strings.TrimSpace(egg.Startup), StartupShell: "/bin/sh", WorkingRoot: "server_root", ResourceDefaults: ContainerResourceDefaults{MemoryLimitBytes: 1 << 30, CPULimitMillis: 1000, PIDsLimit: 512, TempSizeBytes: 256 << 20}}
	images := make([]string, 0, len(egg.DockerImages)+1)
	seen := map[string]bool{}
	addImage := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		images = append(images, value)
	}
	for _, value := range egg.DockerImages {
		addImage(value)
	}
	if err := ValidateContainerImageReference(egg.Scripts.Installation.Container); err == nil {
		addImage(egg.Scripts.Installation.Container)
	} else if strings.TrimSpace(egg.Scripts.Installation.Container) != "" {
		addContainerFinding(&compatibility, SeverityError, "image", "CONTAINER_IMAGE_INVALID", "The Egg installer image is not a valid image reference")
	}
	sort.Strings(images)
	if len(images) == 0 {
		addContainerFinding(&compatibility, SeverityError, "image", "CONTAINER_IMAGE_MISSING", "The Egg does not declare a usable container image")
	} else if len(images) > MaxContainerImages {
		addContainerFinding(&compatibility, SeverityError, "image", "CONTAINER_IMAGE_LIMIT", "The Egg declares too many container images")
		images = images[:MaxContainerImages]
	}
	for _, image := range images {
		if err := ValidateContainerImageReference(image); err != nil {
			addContainerFinding(&compatibility, SeverityError, "image", "CONTAINER_IMAGE_INVALID", "The Egg declares an invalid container image reference")
			break
		}
	}
	plan.Images = images
	plan.InstallerImage = strings.TrimSpace(egg.Scripts.Installation.Container)
	if plan.InstallerImage == "" && len(images) > 0 {
		plan.InstallerImage = images[0]
	}
	plan.InstallerEntrypoint = strings.TrimSpace(egg.Scripts.Installation.Entrypoint)
	if plan.InstallerEntrypoint == "" {
		plan.InstallerEntrypoint = "/bin/sh"
	}
	if !validContainerEntrypoint(plan.InstallerEntrypoint) {
		addContainerFinding(&compatibility, SeverityError, "installer", "CONTAINER_ENTRYPOINT_UNSUPPORTED", "The Egg installer entrypoint is outside the controlled shell allowlist")
	}
	plan.InstallationScript = egg.Scripts.Installation.Script
	if len(plan.InstallationScript) > MaxContainerScriptBytes {
		addContainerFinding(&compatibility, SeverityError, "installer", "CONTAINER_INSTALL_SCRIPT_TOO_LARGE", "The Egg installation script exceeds the bounded installer limit")
	}
	if strings.TrimSpace(plan.StartupTemplate) == "" {
		addContainerFinding(&compatibility, SeverityError, "startup", "CONTAINER_STARTUP_MISSING", "The Egg does not declare a container startup command")
	} else if !balancedShellText(plan.StartupTemplate) || strings.ContainsRune(plan.StartupTemplate, 0) || strings.Contains(plan.StartupTemplate, "$(") || strings.ContainsRune(plan.StartupTemplate, '`') {
		addContainerFinding(&compatibility, SeverityError, "startup", "CONTAINER_STARTUP_MALFORMED", "The Egg startup command is not a bounded container-internal shell plan")
	} else if _, err := Expand(plan.StartupTemplate, defaultVariableValues(variables), known); err != nil {
		addContainerFinding(&compatibility, SeverityError, "startup", "CONTAINER_STARTUP_VARIABLE_UNSUPPORTED", "The Egg startup command references an unknown or unsupported variable")
	}
	if operations, ok := analyzeContainerConfig(egg.Config, known); ok {
		plan.ConfigOperations = operations
	} else if meaningfulContainerConfig(egg.Config) {
		addContainerFinding(&compatibility, SeverityError, "config", "CONTAINER_CONFIG_SEMANTICS_UNSUPPORTED", "The Egg requires configuration semantics that GameNode does not understand safely")
	}
	if len(egg.Scripts.Installation.Script) > 0 {
		addContainerFinding(&compatibility, SeverityInfo, "installer", "CONTAINER_INSTALL_SCRIPT_SANDBOXED", "The Egg installation script is available only inside the unprivileged installer container")
	}
	finalizeCompatibilityValue(&compatibility)
	return plan, compatibility
}

func defaultVariableValues(variables []TemplateVariable) map[string]string {
	values := make(map[string]string, len(variables))
	for _, variable := range variables {
		values[variable.Key] = variable.DefaultValue
	}
	return values
}

func validContainerEntrypoint(value string) bool {
	switch value {
	case "sh", "bash", "/bin/sh", "/bin/bash":
		return true
	default:
		return false
	}
}

func balancedShellText(value string) bool {
	quote := rune(0)
	escaped := false
	for _, r := range value {
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
		}
	}
	return quote == 0 && !escaped
}

func addContainerFinding(compatibility *Compatibility, severity, component, code, summary string) {
	if len(compatibility.Findings) < MaxFindings {
		compatibility.Findings = append(compatibility.Findings, Finding{Severity: severity, Component: component, Code: code, Summary: summary})
	}
}

func finalizeCompatibilityValue(compatibility *Compatibility) {
	compatibility.Status = Compatible
	for _, finding := range compatibility.Findings {
		if finding.Severity == SeverityError {
			compatibility.Status = Unsupported
			return
		}
		if finding.Severity == SeverityWarning {
			compatibility.Status = PartiallyCompatible
		}
	}
}

func analyzeContainerConfig(config map[string]json.RawMessage, known map[string]bool) ([]ContainerConfigOperation, bool) {
	raw, ok := config["files"]
	if !ok {
		return nil, len(config) == 0
	}
	for key, value := range config {
		if key != "files" && meaningfulConfig(value) {
			return nil, false
		}
	}
	if !meaningfulConfig(raw) {
		return nil, true
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var files map[string]struct {
		Parser   string            `json:"parser"`
		Find     map[string]string `json:"find"`
		Replace  map[string]string `json:"replace"`
		Required bool              `json:"required"`
	}
	if json.Unmarshal(raw, &files) != nil || len(files) > 32 {
		return nil, false
	}
	var operations []ContainerConfigOperation
	for target, definition := range files {
		path := strings.TrimSpace(target)
		if _, err := ExpandRelativePath(path, defaultVariableValuesFromKnown(known), known); err != nil || path == "" || len(path) > 240 {
			return nil, false
		}
		switch definition.Parser {
		case "properties", "key-value", "ini":
			for key, value := range definition.Find {
				if _, err := Expand(value, defaultVariableValuesFromKnown(known), known); err != nil {
					return nil, false
				}
				operations = append(operations, ContainerConfigOperation{Format: "properties", Target: path, Key: key, Property: value, Required: definition.Required})
			}
			for key, value := range definition.Replace {
				if _, err := Expand(value, defaultVariableValuesFromKnown(known), known); err != nil {
					return nil, false
				}
				operations = append(operations, ContainerConfigOperation{Format: "properties", Target: path, Key: key, Property: value, Required: definition.Required})
			}
		case "json":
			for key, value := range definition.Replace {
				if _, err := Expand(value, defaultVariableValuesFromKnown(known), known); err != nil {
					return nil, false
				}
				operations = append(operations, ContainerConfigOperation{Format: "json", Target: path, Key: key, Property: value, Required: definition.Required})
			}
		default:
			return nil, false
		}
	}
	if len(operations) == 0 {
		return nil, false
	}
	return operations, true
}

func defaultVariableValuesFromKnown(known map[string]bool) map[string]string {
	values := make(map[string]string, len(known))
	for key := range known {
		values[key] = "value"
	}
	return values
}

func meaningfulContainerConfig(config map[string]json.RawMessage) bool {
	for _, raw := range config {
		if meaningfulConfig(raw) {
			return true
		}
	}
	return false
}

func boundedStrings(values []string) []string {
	if len(values) > 64 {
		return append([]string(nil), values[:64]...)
	}
	return append([]string(nil), values...)
}

func startupReferences(startup, key string) bool {
	for _, match := range placeholder.FindAllStringSubmatch(startup, -1) {
		candidate := match[1]
		if candidate == "" {
			candidate = match[2]
		}
		if candidate == key {
			return true
		}
	}
	return false
}

func checkJSONDepth(data []byte, limit int) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, bytes.ErrTooLarge) {
				return errors.New("invalid egg JSON")
			}
			break
		}
		if delimiter, ok := token.(json.Delim); ok {
			if delimiter == '{' || delimiter == '[' {
				depth++
				if depth > limit {
					return errors.New("egg JSON nesting limit exceeded")
				}
			} else {
				depth--
			}
		}
	}
	if depth != 0 {
		return errors.New("invalid egg JSON")
	}
	return nil
}

func parseVariable(v eggVariable) (TemplateVariable, []string, error) {
	result := TemplateVariable{Name: strings.TrimSpace(v.Name), Description: strings.TrimSpace(v.Description), Key: strings.TrimSpace(v.EnvVariable), DefaultValue: v.DefaultValue, UserViewable: v.UserViewable, UserEditable: v.UserEditable, Type: "string", Sensitive: sensitiveKey.MatchString(v.EnvVariable)}
	var warnings []string
	for _, raw := range strings.Split(v.Rules, "|") {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			continue
		}
		result.RawRules = append(result.RawRules, rule)
		name, argument, _ := strings.Cut(rule, ":")
		switch name {
		case "required":
			result.Required = true
		case "nullable":
			result.Nullable = true
		case "integer":
			result.Type = "integer"
		case "numeric":
			result.Type = "number"
		case "string":
		case "boolean":
			result.Type = "boolean"
		case "between":
			parts := strings.Split(argument, ",")
			if len(parts) != 2 {
				warnings = append(warnings, rule)
				continue
			}
			min, e1 := strconv.ParseFloat(parts[0], 64)
			max, e2 := strconv.ParseFloat(parts[1], 64)
			if e1 != nil || e2 != nil || min > max {
				warnings = append(warnings, rule)
				continue
			}
			if result.Type == "string" {
				minimum, maximum := int(min), int(max)
				result.Validation.MinLength, result.Validation.MaxLength = &minimum, &maximum
			} else {
				result.Validation.Min, result.Validation.Max = &min, &max
			}
		case "min", "max":
			value, err := strconv.ParseFloat(argument, 64)
			if err != nil {
				warnings = append(warnings, rule)
				continue
			}
			if result.Type == "string" {
				length := int(value)
				if value < 0 || float64(length) != value {
					warnings = append(warnings, rule)
					continue
				}
				if name == "min" {
					result.Validation.MinLength = &length
				} else {
					result.Validation.MaxLength = &length
				}
			} else if name == "min" {
				result.Validation.Min = &value
			} else {
				result.Validation.Max = &value
			}
		case "in":
			result.Validation.Allowed = strings.Split(argument, ",")
			result.Type = "enum"
		default:
			warnings = append(warnings, rule)
		}
	}
	if strings.EqualFold(v.FieldType, "select") && len(result.Validation.Allowed) > 0 {
		result.Type = "enum"
	}
	if result.Sensitive {
		result.Type = "secret"
		result.DefaultValue = ""
	}
	if err := validateValue(result, result.DefaultValue); err != nil && result.DefaultValue != "" {
		return TemplateVariable{}, nil, fmt.Errorf("default value does not satisfy imported rules: %w", err)
	}
	return result, warnings, nil
}

func detectInstaller(egg eggDocument, variables []TemplateVariable, result *Template) InstallerDefinition {
	values := map[string]string{}
	defined := map[string]bool{}
	for _, variable := range variables {
		values[variable.Key] = variable.DefaultValue
		defined[variable.Key] = true
	}
	appID, err := strconv.Atoi(values["SRCDS_APPID"])
	if err != nil || appID <= 0 {
		addFinding(result, SeverityError, "installer", "STEAMCMD_APP_ID_MISSING", "A positive SRCDS_APPID is required for a native SteamCMD plan")
		return InstallerDefinition{Type: InstallerUnsupported}
	}
	script := strings.ToLower(egg.Scripts.Installation.Script)
	if !strings.Contains(script, "steamcmd") || !strings.Contains(script, "+app_update") {
		addFinding(result, SeverityError, "installer", "INSTALLER_PATTERN_UNSUPPORTED", "No supported native installer pattern was recognized")
		return InstallerDefinition{Type: InstallerUnsupported}
	}
	plan := &SteamCMDPlan{AppID: appID, Validate: strings.Contains(script, " validate"), LoginMode: "anonymous", Platform: "native", InstallTarget: "server_root"}
	if _, ok := values["SRCDS_BETAID"]; ok {
		plan.BetaBranchVariable = "SRCDS_BETAID"
	}
	if _, ok := values["SRCDS_BETAPASS"]; ok {
		plan.BetaPasswordVariable = "SRCDS_BETAPASS"
	}
	if values["WINDOWS_INSTALL"] == "1" {
		plan.Platform = "windows"
	}
	if defined["WINDOWS_INSTALL"] {
		plan.PlatformVariable = "WINDOWS_INSTALL"
	}
	if defined["STEAM_USER"] || defined["STEAM_PASS"] || defined["STEAM_AUTH"] {
		plan.LoginMode = "anonymous_or_credentials"
	}
	if defined["STEAM_USER"] {
		plan.UsernameVariable = "STEAM_USER"
	}
	if defined["STEAM_PASS"] {
		plan.PasswordVariable = "STEAM_PASS"
	}
	if defined["STEAM_AUTH"] {
		plan.AuthVariable = "STEAM_AUTH"
	}
	if values["STEAM_USER"] != "" {
		plan.LoginMode = "credentials_required"
		addFinding(result, SeverityWarning, "installer", "STEAM_CREDENTIALS_REQUIRED", "The Egg indicates authenticated SteamCMD login; credentials are not stored in the template")
	}
	if defined["INSTALL_FLAGS"] {
		addFinding(result, SeverityWarning, "installer", "UNSUPPORTED_STEAMCMD_OPTIONS", "Free-form INSTALL_FLAGS are not included in the native SteamCMD plan")
	}
	addFinding(result, SeverityInfo, "installer", "SUPPORTED_STEAMCMD_INSTALL", "A native SteamCMD installation plan was derived; Egg shell code is not used")
	return InstallerDefinition{Type: InstallerSteamCMD, SteamCMD: plan}
}

func addFinding(template *Template, severity, component, code, summary string) {
	if len(template.Compatibility.Findings) < MaxFindings {
		template.Compatibility.Findings = append(template.Compatibility.Findings, Finding{severity, component, code, summary})
	}
}
func finalizeCompatibility(template *Template) {
	finalizeCompatibilityValue(&template.Compatibility)
}
