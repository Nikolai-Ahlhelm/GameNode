package templates

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func directResolverFixture(t *testing.T) (Template, string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin", "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "server.exe"), []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	template := Template{
		SchemaVersion: 2,
		Platforms:     []string{"windows"},
		PlatformLaunches: map[string]LaunchDefinition{"windows": {
			Executable:       "bin/server.exe",
			WorkingRoot:      "server_root",
			WorkingDirectory: "bin",
			Arguments:        []string{"--port", "{{PORT}}", "--message=A & B; still one argv"},
			Environment:      map[string]string{"SERVER_PASSWORD": "{{PASSWORD}}"},
			StopMethod:       "stdin_command",
			StopCommand:      "quit",
			StopTimeout:      30,
		}},
		Variables: []TemplateVariable{
			{Name: "Port", Key: "PORT", Type: "integer", DefaultValue: "27015", Required: true, UserEditable: true},
			{Name: "Password", Key: "PASSWORD", Type: "secret", Sensitive: true, Nullable: true, UserEditable: true},
		},
		ExpectedFiles: []ExpectedFile{{Path: "bin/server.exe", Type: "file", Required: true, Platform: "windows"}, {Path: "bin/data", Type: "directory", Required: true}},
	}
	return template, root, map[string]string{"PORT": "27016", "PASSWORD": "do-not-print"}
}

func TestResolveLaunchNormalizesStructuredDefinition(t *testing.T) {
	template, root, values := directResolverFixture(t)
	resolved, err := ResolveLaunch(template, "windows", values, root)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(resolved.Executable) || resolved.Arguments[1] != "27016" || resolved.Arguments[2] != "--message=A & B; still one argv" || resolved.Environment["SERVER_PASSWORD"] != values["PASSWORD"] || resolved.StopCommand != "quit" {
		t.Fatalf("unexpected resolution: %#v", resolved)
	}
}

func TestResolveLaunchRejectsMissingVariablesAndArtifacts(t *testing.T) {
	template, root, values := directResolverFixture(t)
	delete(values, "PORT")
	if _, err := ResolveLaunch(template, "windows", values, root); ValidationCode(err) != CodeInvalidPlatformLaunch {
		t.Fatalf("missing variable code = %v", err)
	}
	values["PORT"] = "27016"
	if err := os.Remove(filepath.Join(root, "bin", "data")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLaunch(template, "windows", values, root); ValidationCode(err) != CodeExpectedFileInvalid {
		t.Fatalf("missing artifact code = %v", err)
	}
}

func TestExpectedFilesRejectTraversalAbsoluteAndWrongType(t *testing.T) {
	known := map[string]bool{"ROOT": true}
	for _, candidate := range []string{"../outside", "/etc/passwd", `C:\\Windows\\system32`, `\\\\server\\share`, "{{UNKNOWN}}/file"} {
		if err := validateExpectedFiles([]ExpectedFile{{Path: candidate, Type: "file", Required: true}}, known, []string{"windows"}); ValidationCode(err) != CodeExpectedFileInvalid {
			t.Fatalf("unsafe expected path %q: %v", candidate, err)
		}
	}
	template, root, values := directResolverFixture(t)
	template.ExpectedFiles = []ExpectedFile{{Path: "bin/data", Type: "file", Required: true}}
	if err := ValidateExpectedFiles(template, "windows", values, root); ValidationCode(err) != CodeExpectedFileInvalid {
		t.Fatalf("wrong artifact type: %v", err)
	}
}

func TestExpectedFilesRejectSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation may require privileges; filesystem platform tests cover reparse escapes")
	}
	template, root, values := directResolverFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	template.ExpectedFiles = []ExpectedFile{{Path: "escape", Type: "file", Required: true}}
	if err := ValidateExpectedFiles(template, "windows", values, root); ValidationCode(err) != CodeExpectedFileInvalid {
		t.Fatalf("symlink escape accepted: %v", err)
	}
}

func TestVariableDefinitionTypesAndConstraints(t *testing.T) {
	minimum, maximum := float64(1), float64(10)
	minLength, maxLength := 1, 16
	valid := []TemplateVariable{
		{Name: "Text", Key: "TEXT", Type: "string", DefaultValue: "x", Required: true, Validation: Validation{MinLength: &minLength, MaxLength: &maxLength}},
		{Name: "Integer", Key: "INTEGER", Type: "integer", DefaultValue: "2", Required: true, Validation: Validation{Min: &minimum, Max: &maximum}},
		{Name: "Number", Key: "NUMBER", Type: "number", DefaultValue: "2.5", Required: true, Validation: Validation{Min: &minimum, Max: &maximum}},
		{Name: "Boolean", Key: "BOOL", Type: "boolean", DefaultValue: "true", Required: true},
		{Name: "Enum", Key: "ENUM", Type: "enum", DefaultValue: "a", Required: true, Validation: Validation{Allowed: []string{"a", "b"}}},
		{Name: "Secret", Key: "SECRET", Type: "secret", Sensitive: true, Nullable: true},
	}
	for _, variable := range valid {
		if err := validateVariableDefinition(variable); err != nil {
			t.Fatalf("valid %s variable: %v", variable.Type, err)
		}
	}
	invalid := valid[1]
	invalid.DefaultValue = "11"
	if err := validateVariableDefinition(invalid); ValidationCode(err) != CodeInvalidVariable {
		t.Fatalf("invalid default code: %v", err)
	}
	invalid = valid[4]
	invalid.Validation.Allowed = nil
	if err := validateVariableDefinition(invalid); ValidationCode(err) != CodeInvalidVariable {
		t.Fatalf("invalid enum code: %v", err)
	}
	invalid = valid[0]
	invalid.Sensitive = true
	if err := validateVariableDefinition(invalid); ValidationCode(err) != CodeInvalidVariable {
		t.Fatalf("invalid sensitivity code: %v", err)
	}
}

func TestHostRequirementsSeparateCompatibilityFromAvailability(t *testing.T) {
	template := Template{Requirements: []TemplateRequirement{{Type: "architecture", Level: "hard", Value: "never-real", Description: "fixture"}}}
	err := CheckHostRequirements(template, runtime.GOOS, runtime.GOARCH)
	if ValidationCode(err) != CodeRequirementUnavailable || strings.Contains(err.Error(), runtime.GOARCH) {
		t.Fatalf("unsafe or unstable requirement error: %v", err)
	}
	if !errors.As(err, new(*ValidationError)) {
		t.Fatalf("requirement error is not typed: %T", err)
	}
}
