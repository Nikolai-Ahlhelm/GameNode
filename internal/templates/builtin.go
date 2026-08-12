package templates

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed builtin/*.json
var builtinFiles embed.FS

func loadBuiltins() (map[string]Template, error) {
	entries, err := fs.Glob(builtinFiles, "builtin/*.json")
	if err != nil {
		return nil, err
	}
	result := make(map[string]Template, len(entries))
	for _, name := range entries {
		data, err := builtinFiles.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var template Template
		if err = json.Unmarshal(data, &template); err != nil {
			return nil, fmt.Errorf("decode built-in template %s: %w", name, err)
		}
		if template.ID == "" || template.Name == "" || template.SourceType != SourceBuiltin || !template.ReadOnly || template.Launch == nil {
			return nil, fmt.Errorf("invalid built-in template %s", name)
		}
		if _, exists := result[template.ID]; exists {
			return nil, fmt.Errorf("duplicate built-in template %s", template.ID)
		}
		result[template.ID] = template
	}
	return result, nil
}

func sortedBuiltins(items map[string]Template) []Template {
	result := make([]Template, 0, len(items))
	for _, template := range items {
		result = append(result, template)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result
}

var ErrBuiltinReadOnly = errors.New("built-in templates are read-only")
