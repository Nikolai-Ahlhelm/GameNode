package templates

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	SourceBuiltin            = "builtin"
	SourceOfficial           = "official"
	SourcePelicanPterodactyl = "pelican-pterodactyl"
	InstallerExisting        = "existing"
	InstallerExistingFiles   = "existing-files"
	InstallerSteamCMD        = "steamcmd"
	InstallerUnsupported     = "unsupported"
	Compatible               = "compatible"
	PartiallyCompatible      = "partially_compatible"
	Unsupported              = "unsupported"
	SeverityInfo             = "info"
	SeverityWarning          = "warning"
	SeverityError            = "error"
	MaxEggBytes              = 256 << 10
	MaxVariables             = 128
	MaxFindings              = 128
	MaxStringBytes           = 16 << 10
	MaxContainerScriptBytes  = 64 << 10
	MaxContainerImages       = 32
	MaxNestingDepth          = 32

	// Adapter formats. The first three edit a game-owned file in place; the
	// fourth stores typed values in GameNode and binds them to the native
	// launch. Adapter schema v2 is required for ManagedLaunch.
	FormatXMLProperties = "xml-properties"
	FormatINIKeyValues  = "ini-key-values"
	FormatSectionTuple  = "section-tuple-key-values"
	FormatManagedLaunch = "managed-launch"

	// Binding types are a closed whitelist. There is deliberately no
	// expression, template, or index-based argument manipulation.
	BindingLaunchValue       = "launch-value"
	BindingLaunchFlag        = "launch-flag"
	BindingLaunchSecret      = "launch-secret"
	BindingEnvironmentValue  = "environment-value"
	BindingEnvironmentSecret = "environment-secret"

	AdapterSchemaVersion = 2
)

type Template struct {
	SchemaVersion       int                         `json:"schema_version,omitempty"`
	ID                  string                      `json:"id,omitempty"`
	Name                string                      `json:"name"`
	Description         string                      `json:"description"`
	Version             string                      `json:"version,omitempty"`
	Category            string                      `json:"category,omitempty"`
	MinimumGameNode     string                      `json:"minimum_gamenode_version,omitempty"`
	KnownLimitations    []string                    `json:"known_limitations,omitempty"`
	Tags                []string                    `json:"tags,omitempty"`
	Icon                string                      `json:"icon,omitempty"`
	SourceType          string                      `json:"source_type"`
	SourceIdentifier    string                      `json:"source_identifier,omitempty"`
	SourceFormatVersion string                      `json:"source_format_version,omitempty"`
	SourceMetadata      SourceMetadata              `json:"source_metadata"`
	Installer           InstallerDefinition         `json:"installer"`
	Launch              *LaunchDefinition           `json:"launch,omitempty"`
	PlatformLaunches    map[string]LaunchDefinition `json:"platform_launches,omitempty"`
	Variables           []TemplateVariable          `json:"variables"`
	Ports               []TemplatePort              `json:"ports,omitempty"`
	ExpectedFiles       []ExpectedFile              `json:"expected_files,omitempty"`
	ConfigFiles         []ConfigFileMetadata        `json:"config_files,omitempty"`
	Requirements        []TemplateRequirement       `json:"requirements,omitempty"`
	Help                *TemplateHelp               `json:"help,omitempty"`
	Configuration       *ConfigurationDefinition    `json:"configuration,omitempty"`
	ResolvedAdapters    []ConfigAdapterDefinition   `json:"-"`
	Compatibility       Compatibility               `json:"compatibility"`
	// Compatibility is retained as the native compatibility alias for
	// backwards-compatible API clients. New clients should use the explicit
	// two-dimensional fields below.
	NativeCompatibility    Compatibility            `json:"native_compatibility"`
	ContainerCompatibility Compatibility            `json:"container_compatibility"`
	ContainerRuntime       *ContainerEggRuntimePlan `json:"container_runtime,omitempty"`
	ReadOnly               bool                     `json:"read_only"`
	Platforms              []string                 `json:"platforms,omitempty"`
	Prerequisites          []string                 `json:"prerequisites,omitempty"`
	CreatedAt              time.Time                `json:"created_at,omitempty"`
	UpdatedAt              time.Time                `json:"updated_at,omitempty"`
}

type ConfigurationDefinition struct {
	Adapters []ConfigAdapterReference `json:"adapters"`
}

type ConfigAdapterReference struct {
	ID            string `json:"id"`
	SchemaVersion int    `json:"schema_version"`
	File          string `json:"file"`
}

// ConfigAdapterDefinition is declarative data. Its Format selects code shipped
// with GameNode; definitions never provide parsers, expressions, or executable hooks.
type ConfigAdapterDefinition struct {
	SchemaVersion     int                          `json:"schema_version"`
	ID                string                       `json:"id"`
	Version           string                       `json:"version"`
	Format            string                       `json:"format"`
	Target            string                       `json:"target"`
	Section           string                       `json:"section,omitempty"`
	ContainerProperty string                       `json:"container_property,omitempty"`
	Initialization    *ConfigAdapterInitialization `json:"initialization,omitempty"`
	RestartRequired   bool                         `json:"restart_required"`
	PostStartOnly     bool                         `json:"post_start_only,omitempty"`
	Fields            []ConfigAdapterField         `json:"fields"`
}

type ConfigAdapterInitialization struct {
	Mode   string `json:"mode"`
	Source string `json:"source"`
}

// ConfigAdapterField describes one semantic game setting. Property names the
// technical record inside a game-owned file; Binding instead names a reviewed
// launch or environment representation. Exactly one of them is populated.
type ConfigAdapterField struct {
	Key         string                `json:"key"`
	Label       string                `json:"label"`
	Description string                `json:"description,omitempty"`
	Section     string                `json:"section,omitempty"`
	Type        string                `json:"type"`
	Property    string                `json:"property,omitempty"`
	Binding     *ConfigAdapterBinding `json:"binding,omitempty"`
	Required    bool                  `json:"required"`
	Nullable    bool                  `json:"nullable"`
	Sensitive   bool                  `json:"sensitive"`
	Validation  Validation            `json:"validation"`
}

// ConfigAdapterBinding is reviewed Official data, never user input. Argument
// and Name are fixed by the adapter author; a user only supplies the value.
type ConfigAdapterBinding struct {
	Type       string `json:"type"`
	Argument   string `json:"argument,omitempty"`
	Name       string `json:"name,omitempty"`
	TrueValue  string `json:"true_value,omitempty"`
	FalseValue string `json:"false_value,omitempty"`
}

// LaunchBinding reports whether the binding contributes argv elements rather
// than environment entries.
func (b ConfigAdapterBinding) LaunchBinding() bool {
	return b.Type == BindingLaunchValue || b.Type == BindingLaunchFlag || b.Type == BindingLaunchSecret
}

// SecretBinding reports whether the binding carries a value that must never be
// persisted into a resolved launch snapshot, API response, audit record, or log.
func (b ConfigAdapterBinding) SecretBinding() bool {
	return b.Type == BindingLaunchSecret || b.Type == BindingEnvironmentSecret
}

// ExpandRelativePath is the only expansion helper intended for future file
// plans. It rejects absolute and traversal results before any filesystem use.
func ExpandRelativePath(value string, variables map[string]string, known map[string]bool) (string, error) {
	expanded, err := Expand(value, variables, known)
	if err != nil {
		return "", err
	}
	normalized := strings.ReplaceAll(expanded, "\\", "/")
	clean := filepath.ToSlash(filepath.Clean(normalized))
	if filepath.IsAbs(expanded) || strings.HasPrefix(normalized, "/") || strings.HasPrefix(normalized, "//") || (len(normalized) >= 2 && normalized[1] == ':') || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("expanded path escapes the server root")
	}
	return clean, nil
}

type SourceMetadata struct {
	Author        string            `json:"author,omitempty"`
	ExportedAt    string            `json:"exported_at,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Features      []string          `json:"features,omitempty"`
	DockerImages  map[string]string `json:"docker_images,omitempty"`
	OriginalHash  string            `json:"original_sha256"`
	IgnoredFields []string          `json:"ignored_fields,omitempty"`
}

type InstallerDefinition struct {
	Type     string        `json:"type"`
	SteamCMD *SteamCMDPlan `json:"steamcmd,omitempty"`
}

type SteamCMDPlan struct {
	AppID                int    `json:"app_id"`
	Validate             bool   `json:"validate"`
	LoginMode            string `json:"login_mode"`
	Platform             string `json:"platform"`
	BetaBranchVariable   string `json:"beta_branch_variable,omitempty"`
	BetaPasswordVariable string `json:"beta_password_variable,omitempty"`
	UsernameVariable     string `json:"username_variable,omitempty"`
	PasswordVariable     string `json:"password_variable,omitempty"`
	AuthVariable         string `json:"auth_variable,omitempty"`
	PlatformVariable     string `json:"platform_variable,omitempty"`
	InstallTarget        string `json:"install_target"`
}

type LaunchDefinition struct {
	Executable       string            `json:"executable"`
	Arguments        []string          `json:"arguments"`
	WorkingRoot      string            `json:"working_root"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
	StopCommand      string            `json:"stop_command,omitempty"`
	StopMethod       string            `json:"stop_method,omitempty"`
	StopTimeout      int               `json:"stop_timeout_seconds,omitempty"`
	Resolver         string            `json:"resolver,omitempty"`
	Environment      map[string]string `json:"environment,omitempty"`
}

type TemplatePort struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	Port          int    `json:"port,omitempty"`
	ContainerPort int    `json:"container_port,omitempty"`
	Variable      string `json:"variable,omitempty"`
	Offset        int    `json:"offset,omitempty"`
	Required      bool   `json:"required,omitempty"`
	Purpose       string `json:"purpose,omitempty"`
}

// ExpectedFile describes an artifact that must remain below the server root.
// Platform is optional; an empty value applies to every supported platform.
type ExpectedFile struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Required   bool   `json:"required"`
	Executable bool   `json:"executable,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

// ConfigFileMetadata is documentation-only. It never selects a parser or
// causes GameNode to edit a file.
type ConfigFileMetadata struct {
	Path        string `json:"path"`
	Format      string `json:"format"`
	Description string `json:"description,omitempty"`
}

// TemplateRequirement separates hard host checks from informational hints.
// Only the small, compiled type catalog is interpreted by GameNode.
type TemplateRequirement struct {
	Type        string `json:"type"`
	Level       string `json:"level"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description"`
}

type TemplateHelp struct {
	Summary string   `json:"summary,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// LaunchForPlatform selects an explicit host launch when one is present and
// otherwise preserves the normalized single-launch model used by Egg imports.
func LaunchForPlatform(template Template, platform string) (*LaunchDefinition, bool) {
	if len(template.PlatformLaunches) > 0 {
		launch, ok := template.PlatformLaunches[platform]
		if !ok {
			return nil, false
		}
		return &launch, true
	}
	return template.Launch, template.Launch != nil
}

type TemplateVariable struct {
	Name         string     `json:"name"`
	Description  string     `json:"description,omitempty"`
	Key          string     `json:"key"`
	DefaultValue string     `json:"default_value,omitempty"`
	UserViewable bool       `json:"user_viewable"`
	UserEditable bool       `json:"user_editable"`
	Type         string     `json:"type"`
	Sensitive    bool       `json:"sensitive"`
	Required     bool       `json:"required"`
	Nullable     bool       `json:"nullable"`
	Validation   Validation `json:"validation"`
	RawRules     []string   `json:"raw_rules,omitempty"`
	Placeholder  string     `json:"placeholder,omitempty"`
	Advanced     bool       `json:"advanced,omitempty"`
	Group        string     `json:"group,omitempty"`
}

type Validation struct {
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	MinLength *int     `json:"min_length,omitempty"`
	MaxLength *int     `json:"max_length,omitempty"`
	Allowed   []string `json:"allowed,omitempty"`
}

type Compatibility struct {
	Status   string    `json:"status"`
	Findings []Finding `json:"findings"`
}

func NormalizeCompatibility(template *Template) {
	if template.NativeCompatibility.Status == "" {
		template.NativeCompatibility = template.Compatibility
	}
	if template.Compatibility.Status == "" {
		template.Compatibility = template.NativeCompatibility
	}
}

// ContainerEggRuntimePlan is a normalized, bounded representation of the
// container-only Egg semantics. It is data, never a host command. The
// installation script is deliberately retained only in this plan so it can
// be executed inside the controlled installer container; it must never be
// copied to audit, logs, or a host exec call.
type ContainerEggRuntimePlan struct {
	Images              []string                   `json:"images"`
	InstallerImage      string                     `json:"installer_image,omitempty"`
	InstallerEntrypoint string                     `json:"installer_entrypoint,omitempty"`
	InstallationScript  string                     `json:"installation_script,omitempty"`
	StartupTemplate     string                     `json:"startup_template"`
	StartupShell        string                     `json:"startup_shell"`
	WorkingRoot         string                     `json:"working_root"`
	ConfigOperations    []ContainerConfigOperation `json:"config_operations,omitempty"`
	ResourceDefaults    ContainerResourceDefaults  `json:"resource_defaults"`
}

type ContainerConfigOperation struct {
	Format   string `json:"format"`
	Target   string `json:"target"`
	Key      string `json:"key"`
	Property string `json:"property,omitempty"`
	Required bool   `json:"required,omitempty"`
}

type ContainerResourceDefaults struct {
	MemoryLimitBytes int64 `json:"memory_limit_bytes"`
	CPULimitMillis   int   `json:"cpu_limit_millis"`
	PIDsLimit        int64 `json:"pids_limit"`
	TempSizeBytes    int64 `json:"temp_size_bytes"`
}

type Finding struct {
	Severity  string `json:"severity"`
	Component string `json:"component"`
	Code      string `json:"code"`
	Summary   string `json:"summary"`
}

var placeholder = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}|\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand performs exact, allowlisted placeholder replacement. It never invokes
// a shell and never expands host environment variables.
func Expand(value string, variables map[string]string, known map[string]bool) (string, error) {
	if strings.ContainsRune(value, 0) {
		return "", errors.New("template value contains NUL")
	}
	if strings.Contains(value, "$(") || strings.ContainsRune(value, '`') {
		return "", errors.New("command substitution is not supported")
	}
	var expandErr error
	result := placeholder.ReplaceAllStringFunc(value, func(token string) string {
		match := placeholder.FindStringSubmatch(token)
		key := match[1]
		if key == "" {
			key = match[2]
		}
		if !known[key] {
			expandErr = fmt.Errorf("unknown template variable %s", key)
			return ""
		}
		return variables[key]
	})
	if expandErr != nil {
		return "", expandErr
	}
	if strings.Contains(result, "${") || strings.Contains(result, "{{") {
		return "", errors.New("malformed or unsupported template placeholder")
	}
	return result, nil
}

func validateValue(variable TemplateVariable, value string) error {
	if value == "" && variable.Nullable {
		return nil
	}
	if value == "" && variable.Required {
		return errors.New("value is required")
	}
	var number float64
	var err error
	switch variable.Type {
	case "integer":
		var integer int64
		integer, err = strconv.ParseInt(value, 10, 64)
		number = float64(integer)
	case "number":
		number, err = strconv.ParseFloat(value, 64)
	case "boolean":
		if value != "0" && value != "1" && !strings.EqualFold(value, "true") && !strings.EqualFold(value, "false") {
			err = errors.New("expected boolean")
		}
	}
	if err != nil {
		return err
	}
	if variable.Validation.Min != nil && number < *variable.Validation.Min {
		return errors.New("value is below minimum")
	}
	if variable.Validation.Max != nil && number > *variable.Validation.Max {
		return errors.New("value is above maximum")
	}
	if variable.Validation.MinLength != nil && len(value) < *variable.Validation.MinLength {
		return errors.New("value is shorter than minimum length")
	}
	if variable.Validation.MaxLength != nil && len(value) > *variable.Validation.MaxLength {
		return errors.New("value is longer than maximum length")
	}
	if len(variable.Validation.Allowed) > 0 {
		for _, allowed := range variable.Validation.Allowed {
			if value == allowed {
				return nil
			}
		}
		return errors.New("value is not in allowed values")
	}
	return nil
}

// ResolveValues validates concrete per-server values against the normalized
// template schema. Unknown keys and attempts to override fixed variables are
// rejected; errors contain keys but never submitted values.
func ResolveValues(template Template, supplied map[string]string) (map[string]string, map[string]bool, error) {
	definitions := make(map[string]TemplateVariable, len(template.Variables))
	for _, variable := range template.Variables {
		definitions[variable.Key] = variable
	}
	for key := range supplied {
		if _, ok := definitions[key]; !ok {
			return nil, nil, fmt.Errorf("unknown template variable %s", key)
		}
	}
	values := make(map[string]string, len(definitions))
	sensitive := make(map[string]bool)
	for _, variable := range template.Variables {
		value, provided := supplied[variable.Key]
		if !provided {
			value = variable.DefaultValue
		}
		if provided && !variable.UserEditable && value != variable.DefaultValue {
			return nil, nil, fmt.Errorf("template variable %s is not editable", variable.Key)
		}
		if err := validateValue(variable, value); err != nil {
			return nil, nil, fmt.Errorf("template variable %s is invalid", variable.Key)
		}
		if variable.Type == "boolean" {
			if strings.EqualFold(value, "true") {
				value = "1"
			} else if strings.EqualFold(value, "false") {
				value = "0"
			}
		}
		values[variable.Key] = value
		if variable.Sensitive {
			sensitive[variable.Key] = true
		}
	}
	return values, sensitive, nil
}
