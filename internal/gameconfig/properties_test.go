package gameconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/templates"
)

func propertiesDefinition() templates.ConfigAdapterDefinition {
	min, max := float64(1), float64(100)
	return templates.ConfigAdapterDefinition{SchemaVersion: 1, ID: "props", Version: "1.0.0", Format: templates.FormatPropertiesKeyValues, Target: "server.properties", RestartRequired: true, Fields: []templates.ConfigAdapterField{
		{Key: "MC_MOTD", Label: "MOTD", Type: "string", Property: "motd", Nullable: true},
		{Key: "MC_MAX_PLAYERS", Label: "Players", Type: "integer", Property: "max-players", Required: true, Validation: templates.Validation{Min: &min, Max: &max}},
		{Key: "MC_PVP", Label: "PvP", Type: "boolean", Property: "pvp", Required: true},
		{Key: "MC_LEVEL_TYPE", Label: "Type", Type: "enum", Property: "level-type", Required: true, Validation: templates.Validation{Allowed: []string{"minecraft:normal", "minecraft:flat"}}},
		{Key: "MC_QUERY_PORT", Label: "Query", Type: "integer", Property: "query.port", Required: true},
	}}
}

func TestPropertiesCreatesMissingFileAndWritesJavaBooleans(t *testing.T) {
	root := t.TempDir()
	definition := propertiesDefinition()
	values := map[string]string{"MC_MOTD": "Hello: §World \\ \U0001F600", "MC_MAX_PLAYERS": "12", "MC_PVP": "0", "MC_LEVEL_TYPE": "minecraft:flat", "MC_QUERY_PORT": "25565"}
	if err := Apply(root, definition, values); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "server.properties"))
	text := string(data)
	for _, want := range []string{"max-players=12\n", "pvp=false\n", `level-type=minecraft\:flat` + "\n", `motd=Hello\: \u00a7World \\ \ud83d\ude00` + "\n", "query.port=25565\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	read, err := Read(root, definition)
	if err != nil || read["MC_MOTD"] != "Hello: §World \\ \U0001F600" || read["MC_LEVEL_TYPE"] != "minecraft:flat" || read["MC_PVP"] != "false" {
		t.Fatalf("round trip = %v, %v", read, err)
	}
}

func TestPropertiesPreservesUnmanagedLinesAndAppendsMissingKeys(t *testing.T) {
	root := t.TempDir()
	original := "#Minecraft server properties\r\n#Sat Oct 03 17:00:00 UTC 2026\r\nsome-future-key=keep me\r\nmax-players=20\r\nmotd=A Minecraft Server\r\n"
	if err := os.WriteFile(filepath.Join(root, "server.properties"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	definition := propertiesDefinition()
	read, err := Read(root, definition)
	if err != nil || read["MC_MAX_PLAYERS"] != "20" || read["MC_PVP"] != "" {
		t.Fatalf("partial file must read without error, absent keys empty: %v %v", read, err)
	}
	if err = Apply(root, definition, map[string]string{"MC_MAX_PLAYERS": "50", "MC_PVP": "true"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "server.properties"))
	text := string(data)
	if !strings.Contains(text, "some-future-key=keep me\r\n") || !strings.Contains(text, "max-players=50\r\n") || !strings.Contains(text, "pvp=true\r\n") || !strings.Contains(text, "#Sat Oct 03") || strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Fatalf("unmanaged lines/CRLF not preserved:\n%q", text)
	}
	backup, err := os.ReadFile(filepath.Join(root, ".gamenode-backups", "server.properties.previous"))
	if err != nil || string(backup) != original {
		t.Fatalf("backup = %q, %v", backup, err)
	}
}

func TestPropertiesRejectsInvalidValuesAndUnsafeShapes(t *testing.T) {
	root := t.TempDir()
	definition := propertiesDefinition()
	for name, values := range map[string]map[string]string{
		"out of range": {"MC_MAX_PLAYERS": "1000"},
		"bad enum":     {"MC_LEVEL_TYPE": "minecraft:evil"},
		"newline":      {"MC_MOTD": "line\ninjected=1"},
		"unknown":      {"MC_OTHER": "x"},
		"bad boolean":  {"MC_PVP": "yes"},
	} {
		if err := Apply(root, definition, values); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "server.properties")); err == nil {
		t.Fatal("a rejected update must not create the file")
	}
	bad := propertiesDefinition()
	bad.Target = "server.ini"
	if ValidateDefinition(bad) == nil {
		t.Fatal("properties format must require a .properties target")
	}
	bad = propertiesDefinition()
	bad.PostStartOnly = true
	if ValidateDefinition(bad) == nil {
		t.Fatal("properties format must not be post-start-only")
	}
	bad = propertiesDefinition()
	bad.Fields[0].Property = "Bad_Key"
	if ValidateDefinition(bad) == nil {
		t.Fatal("property names must match the properties key shape")
	}
}

func TestPropertiesDuplicateManagedKeyIsRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "server.properties"), []byte("pvp=true\npvp=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, propertiesDefinition(), map[string]string{"MC_PVP": "true"}); err == nil {
		t.Fatal("duplicate managed key must be rejected")
	}
}
