package templates

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadRepoTemplate(t *testing.T, file string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func validateMutated(t *testing.T, data []byte, mutate func(*Template)) error {
	t.Helper()
	var template Template
	if err := json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	mutate(&template)
	return validateOfficial(template)
}

func TestVintageStoryTemplateContractIsPinned(t *testing.T) {
	data := loadRepoTemplate(t, "vintagestory/template.json")
	if err := validateMutated(t, data, func(*Template) {}); err != nil {
		t.Fatalf("repository template must validate: %v", err)
	}
	for name, mutate := range map[string]func(*Template){
		"wrong resolver":         func(tpl *Template) { tpl.Launch.Resolver = "java" },
		"shell executable":       func(tpl *Template) { tpl.Launch.Executable = "bash" },
		"other executable":       func(tpl *Template) { tpl.Launch.Executable = "mono" },
		"version not editable":   func(tpl *Template) { tpl.Variables[0].UserEditable = false },
		"port wrong type":        func(tpl *Template) { tpl.Variables[1].Type = "string" },
		"missing version":        func(tpl *Template) { tpl.Variables = tpl.Variables[1:] },
		"platform launches":      func(tpl *Template) { tpl.PlatformLaunches = map[string]LaunchDefinition{"linux": *tpl.Launch} },
		"bad console endings":    func(tpl *Template) { tpl.Launch.ConsoleLineEnding = "cr" },
		"unknown stop method":    func(tpl *Template) { tpl.Launch.StopMethod = "signal" },
		"steamcmd plan smuggled": func(tpl *Template) { tpl.Installer.SteamCMD = &SteamCMDPlan{AppID: 1} },
	} {
		if err := validateMutated(t, data, mutate); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestHytaleTemplateIsAdoptOnlyWithFixedLaunchShape(t *testing.T) {
	data := loadRepoTemplate(t, "hytale/template.json")
	if err := validateMutated(t, data, func(*Template) {}); err != nil {
		t.Fatalf("repository template must validate: %v", err)
	}
	var template Template
	if err := json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	if template.Installer.Type != InstallerExistingFiles || template.Launch.Resolver != "java" || template.Configuration != nil {
		t.Fatalf("Hytale must stay adopt-only (no installer, no unverified config adapter): %+v", template.Installer)
	}
}
