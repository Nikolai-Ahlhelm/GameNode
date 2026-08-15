package templates

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	CodeSchemaInvalid           = "TEMPLATE_SCHEMA_INVALID"
	CodeUnsupportedVersion      = "TEMPLATE_UNSUPPORTED_VERSION"
	CodeInvalidPath             = "TEMPLATE_INVALID_PATH"
	CodeUnsupportedInstaller    = "TEMPLATE_UNSUPPORTED_INSTALLER"
	CodeInvalidVariable         = "TEMPLATE_INVALID_VARIABLE"
	CodeInvalidPlatformLaunch   = "TEMPLATE_INVALID_PLATFORM_LAUNCH"
	CodeShellSemanticsForbidden = "TEMPLATE_SHELL_SEMANTICS_FORBIDDEN"
	CodeExpectedFileInvalid     = "TEMPLATE_EXPECTED_FILE_INVALID"
	CodeRequirementUnavailable  = "TEMPLATE_REQUIREMENT_UNAVAILABLE"
)

// ValidationError carries a stable machine code and a safe message. It never
// includes submitted values, host paths, installer output, or secrets.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

func validationError(code, message string) error {
	return &ValidationError{Code: code, Message: message}
}

func ValidationCode(err error) string {
	var target *ValidationError
	if errors.As(err, &target) {
		return target.Code
	}
	return CodeSchemaInvalid
}

func supportedTemplateSchema(version int) bool { return version == 1 || version == 2 }

func validateVariableDefinition(variable TemplateVariable) error {
	if variable.Required && variable.Nullable {
		return validationError(CodeInvalidVariable, "a variable cannot be required and nullable")
	}
	if len(variable.Name) == 0 || len(variable.Name) > 80 || len(variable.Description) > 512 || len(variable.Placeholder) > 160 || len(variable.Group) > 64 {
		return validationError(CodeInvalidVariable, "variable presentation metadata is invalid")
	}
	switch variable.Type {
	case "string", "secret":
		if variable.Validation.Min != nil || variable.Validation.Max != nil || len(variable.Validation.Allowed) != 0 {
			return validationError(CodeInvalidVariable, "string validation contains numeric or enum constraints")
		}
	case "integer", "number":
		if variable.Validation.MinLength != nil || variable.Validation.MaxLength != nil || len(variable.Validation.Allowed) != 0 {
			return validationError(CodeInvalidVariable, "numeric validation contains string or enum constraints")
		}
		if variable.Validation.Min != nil && variable.Validation.Max != nil && *variable.Validation.Min > *variable.Validation.Max {
			return validationError(CodeInvalidVariable, "numeric minimum exceeds maximum")
		}
	case "boolean":
		if variable.Validation.Min != nil || variable.Validation.Max != nil || variable.Validation.MinLength != nil || variable.Validation.MaxLength != nil || len(variable.Validation.Allowed) != 0 {
			return validationError(CodeInvalidVariable, "boolean validation contains unsupported constraints")
		}
	case "enum":
		if len(variable.Validation.Allowed) == 0 || len(variable.Validation.Allowed) > 64 || variable.Validation.Min != nil || variable.Validation.Max != nil || variable.Validation.MinLength != nil || variable.Validation.MaxLength != nil {
			return validationError(CodeInvalidVariable, "enum validation must define only allowed values")
		}
	default:
		return validationError(CodeInvalidVariable, "variable type is unsupported")
	}
	if variable.Sensitive != (variable.Type == "secret") {
		return validationError(CodeInvalidVariable, "sensitive variables must use the secret type")
	}
	if variable.Validation.MinLength != nil && (*variable.Validation.MinLength < 0 || *variable.Validation.MinLength > MaxStringBytes) {
		return validationError(CodeInvalidVariable, "minimum length is invalid")
	}
	if variable.Validation.MaxLength != nil && (*variable.Validation.MaxLength < 0 || *variable.Validation.MaxLength > MaxStringBytes) {
		return validationError(CodeInvalidVariable, "maximum length is invalid")
	}
	if variable.Validation.MinLength != nil && variable.Validation.MaxLength != nil && *variable.Validation.MinLength > *variable.Validation.MaxLength {
		return validationError(CodeInvalidVariable, "minimum length exceeds maximum")
	}
	if err := validateValue(variable, variable.DefaultValue); err != nil {
		return validationError(CodeInvalidVariable, "variable default is invalid")
	}
	return nil
}

func pathValidationValues(known map[string]bool) map[string]string {
	values := make(map[string]string, len(known))
	for key := range known {
		values[key] = "value"
	}
	return values
}

func validateTemplatePath(value string, known map[string]bool) error {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 240 || strings.ContainsAny(value, "\x00\r\n") {
		return validationError(CodeInvalidPath, "template path is empty, oversized, or malformed")
	}
	if _, err := ExpandRelativePath(value, pathValidationValues(known), known); err != nil {
		return validationError(CodeInvalidPath, "template path must remain below the server root")
	}
	return nil
}

func validateExpectedFiles(items []ExpectedFile, known map[string]bool, _ []string) error {
	if len(items) > 64 {
		return validationError(CodeExpectedFileInvalid, "too many expected files are declared")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if err := validateTemplatePath(item.Path, known); err != nil || (item.Type != "file" && item.Type != "directory") || (item.Executable && item.Type != "file") {
			return validationError(CodeExpectedFileInvalid, "expected file definition is invalid")
		}
		if item.Platform != "" && item.Platform != "windows" && item.Platform != "linux" {
			return validationError(CodeExpectedFileInvalid, "expected file platform is unsupported")
		}
		key := item.Platform + ":" + filepath.ToSlash(filepath.Clean(item.Path))
		if seen[key] {
			return validationError(CodeExpectedFileInvalid, "expected file definition is duplicated")
		}
		seen[key] = true
	}
	return nil
}

func validateConfigFiles(items []ConfigFileMetadata, known map[string]bool) error {
	if len(items) > 32 {
		return validationError(CodeInvalidPath, "too many configuration files are declared")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if err := validateTemplatePath(item.Path, known); err != nil || len(item.Format) == 0 || len(item.Format) > 32 || len(item.Description) > 512 || seen[item.Path] {
			return validationError(CodeInvalidPath, "configuration file metadata is invalid")
		}
		seen[item.Path] = true
	}
	return nil
}

func validateRequirements(items []TemplateRequirement) error {
	if len(items) > 32 {
		return validationError(CodeSchemaInvalid, "too many requirements are declared")
	}
	for _, item := range items {
		if item.Level != "hard" && item.Level != "informational" {
			return validationError(CodeSchemaInvalid, "requirement level is invalid")
		}
		switch item.Type {
		case "os", "architecture", "java", "steamcmd", "disk", "note":
		default:
			return validationError(CodeSchemaInvalid, "requirement type is unsupported")
		}
		if len(item.Description) == 0 || len(item.Description) > 512 || len(item.Value) > 128 {
			return validationError(CodeSchemaInvalid, "requirement metadata is invalid")
		}
	}
	return nil
}

// CheckHostRequirements enforces only requirements that can be established
// without guessing. Disk and note requirements remain informational hints.
func CheckHostRequirements(template Template, hostOS, hostArch string) error {
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}
	for _, requirement := range template.Requirements {
		if requirement.Level != "hard" {
			continue
		}
		switch requirement.Type {
		case "os":
			if requirement.Value != hostOS {
				return validationError(CodeRequirementUnavailable, "the host operating system does not satisfy this template")
			}
		case "architecture":
			if requirement.Value != hostArch {
				return validationError(CodeRequirementUnavailable, "the host architecture does not satisfy this template")
			}
		case "java":
			if _, found := DiscoverJava(); !found {
				return validationError(CodeRequirementUnavailable, "Java runtime not found")
			}
		}
	}
	return nil
}

func validateEnvironment(environment map[string]string, known map[string]bool) error {
	if len(environment) > 64 {
		return validationError(CodeInvalidPlatformLaunch, "too many environment entries are declared")
	}
	for key, value := range environment {
		if !officialVariablePattern.MatchString(key) || len(value) > MaxStringBytes {
			return validationError(CodeInvalidPlatformLaunch, "environment entry is invalid")
		}
		if _, err := Expand(value, map[string]string{}, known); err != nil {
			return validationError(CodeInvalidPlatformLaunch, "environment placeholder is invalid")
		}
	}
	return nil
}

func sensitivePlaceholder(value string, definitions map[string]TemplateVariable) bool {
	for key, definition := range definitions {
		if definition.Sensitive && (strings.Contains(value, "{{"+key+"}}") || strings.Contains(value, "${"+key+"}")) {
			return true
		}
	}
	return false
}
