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
	SourcePelicanPterodactyl = "pelican-pterodactyl"
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
	MaxNestingDepth          = 32
)

type Template struct {
	ID                  string              `json:"id,omitempty"`
	Name                string              `json:"name"`
	Description         string              `json:"description"`
	SourceType          string              `json:"source_type"`
	SourceIdentifier    string              `json:"source_identifier,omitempty"`
	SourceFormatVersion string              `json:"source_format_version,omitempty"`
	SourceMetadata      SourceMetadata      `json:"source_metadata"`
	Installer           InstallerDefinition `json:"installer"`
	Launch              *LaunchDefinition   `json:"launch,omitempty"`
	Variables           []TemplateVariable  `json:"variables"`
	Compatibility       Compatibility       `json:"compatibility"`
	CreatedAt           time.Time           `json:"created_at,omitempty"`
	UpdatedAt           time.Time           `json:"updated_at,omitempty"`
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
	Executable  string   `json:"executable"`
	Arguments   []string `json:"arguments"`
	WorkingRoot string   `json:"working_root"`
	StopCommand string   `json:"stop_command,omitempty"`
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
