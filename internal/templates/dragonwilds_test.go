package templates

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDragonwildsRepositoryGolden(t *testing.T) {
	directory := filepath.Join("..", "..", "templates", "steamcmd", "runescape-dragonwilds")
	templateData, err := os.ReadFile(filepath.Join(directory, "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	entry := dragonwildsCatalogEntry(t)
	template, err := decodeOfficial(templateData, entry, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if template.Configuration == nil || len(template.Configuration.Adapters) != 2 {
		t.Fatalf("unexpected configuration: %#v", template.Configuration)
	}
	targets := map[string]string{"windows": "RSDragonwilds/Saved/Config/WindowsServer/DedicatedServer.ini", "linux": "RSDragonwilds/Saved/Config/LinuxServer/DedicatedServer.ini"}
	for _, reference := range template.Configuration.Adapters {
		data, readErr := os.ReadFile(filepath.Join(directory, reference.File))
		if readErr != nil {
			t.Fatal(readErr)
		}
		adapter, adapterErr := decodeConfigAdapter(data, reference, template)
		if adapterErr != nil {
			t.Fatalf("adapter %s: %v", reference.ID, adapterErr)
		}
		if len(adapter.Platforms) != 1 || adapter.Format != FormatINISectionKeyValues || adapter.Section != "/Script/Dominion.DedicatedServerSettings" || !adapter.PostStartOnly || len(adapter.Fields) != 5 || adapter.Target != targets[adapter.Platforms[0]] {
			t.Fatalf("unexpected adapter: %#v", adapter)
		}
		other := map[string]string{"windows": "linux", "linux": "windows"}[adapter.Platforms[0]]
		if !AdapterAppliesTo(adapter, adapter.Platforms[0]) || AdapterAppliesTo(adapter, other) {
			t.Fatalf("platform filter is wrong for %s", adapter.ID)
		}
		for _, field := range adapter.Fields {
			if (field.Property == "AdminPassword" || field.Property == "WorldPassword") != (field.Type == "secret" && field.Sensitive) {
				t.Fatalf("secret handling is wrong for %s", field.Property)
			}
		}
	}
}

func TestAdapterAppliesToWithoutPlatformsAppliesEverywhere(t *testing.T) {
	if !AdapterAppliesTo(ConfigAdapterDefinition{}, "linux") || !AdapterAppliesTo(ConfigAdapterDefinition{}, "windows") {
		t.Fatal("unrestricted adapter must apply to every platform")
	}
}

func TestDecodeConfigAdapterRejectsInvalidPlatforms(t *testing.T) {
	directory := filepath.Join("..", "..", "templates", "steamcmd", "runescape-dragonwilds")
	templateData, _ := os.ReadFile(filepath.Join(directory, "template.json"))
	entry := dragonwildsCatalogEntry(t)
	template, err := decodeOfficial(templateData, entry, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	reference := template.Configuration.Adapters[0]
	data, _ := os.ReadFile(filepath.Join(directory, reference.File))
	var adapter ConfigAdapterDefinition
	if err = json.Unmarshal(data, &adapter); err != nil {
		t.Fatal(err)
	}
	for _, platforms := range [][]string{{"darwin"}, {"windows", "windows"}, {"windows", "linux", "linux"}} {
		adapter.Platforms = platforms
		broken, _ := json.Marshal(adapter)
		if _, err = decodeConfigAdapter(broken, reference, template); err == nil {
			t.Fatalf("invalid adapter platforms %v were accepted", platforms)
		}
	}
}

func dragonwildsCatalogEntry(t *testing.T) CatalogEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodeCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Templates {
		if entry.ID == "runescape-dragonwilds" {
			return entry
		}
	}
	t.Fatal("runescape-dragonwilds is not in the catalog")
	return CatalogEntry{}
}
