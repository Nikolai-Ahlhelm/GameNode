package gameconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/templates"
)

func palworldAdapterFixture() templates.ConfigAdapterDefinition {
	minPort, maxPort, maxLength := float64(1024), float64(65535), 512
	return templates.ConfigAdapterDefinition{
		SchemaVersion: 1, ID: "palworld-settings", Version: "1.0.0", Format: sectionTupleFormat,
		Target: "Pal/Saved/Config/WindowsServer/PalWorldSettings.ini", Section: "/Script/Pal.PalGameWorldSettings", ContainerProperty: "OptionSettings",
		Initialization: &templates.ConfigAdapterInitialization{Mode: "seed-from-file", Source: "DefaultPalWorldSettings.ini"}, RestartRequired: true,
		Fields: []templates.ConfigAdapterField{
			{Key: "SERVER_NAME", Label: "Server name", Type: "string", Property: "ServerName", Required: true, Validation: templates.Validation{MinLength: intPointer(1), MaxLength: &maxLength}},
			{Key: "SERVER_DESCRIPTION", Label: "Server description", Type: "string", Property: "ServerDescription", Nullable: true, Validation: templates.Validation{MaxLength: &maxLength}},
			{Key: "SERVER_PASSWORD", Label: "Server password", Type: "secret", Property: "ServerPassword", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}},
			{Key: "ADMIN_PASSWORD", Label: "Admin password", Type: "secret", Property: "AdminPassword", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}},
			{Key: "RCON_ENABLED", Label: "RCON", Type: "boolean", Property: "RCONEnabled", Required: true},
			{Key: "RCON_PORT", Label: "RCON port", Type: "integer", Property: "RCONPort", Required: true, Validation: templates.Validation{Min: &minPort, Max: &maxPort}},
			{Key: "REST_API_ENABLED", Label: "REST API", Type: "boolean", Property: "RESTAPIEnabled", Required: true},
			{Key: "REST_API_PORT", Label: "REST API port", Type: "integer", Property: "RESTAPIPort", Required: true, Validation: templates.Validation{Min: &minPort, Max: &maxPort}},
			{Key: "BACKUP_ENABLED", Label: "Backups", Type: "boolean", Property: "bIsUseBackupSaveData", Required: true},
		},
	}
}

const (
	palworldTarget   = "Pal/Saved/Config/WindowsServer/PalWorldSettings.ini"
	palworldSeedFrom = "DefaultPalWorldSettings.ini"
)

func palworldFixtureData(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "palworld", "fixtures", "PalWorldSettings.example.ini"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writePalworldTarget(t *testing.T, root string, data []byte) string {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(palworldTarget))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	return target
}

func palworldValues() map[string]string {
	return map[string]string{
		"SERVER_NAME": "Release Server", "SERVER_DESCRIPTION": `Comma, quote " and slash \`,
		"SERVER_PASSWORD": "player-secret", "ADMIN_PASSWORD": "admin-secret",
		"RCON_ENABLED": "true", "RCON_PORT": "25576", "REST_API_ENABLED": "false",
		"REST_API_PORT": "8213", "BACKUP_ENABLED": "false",
	}
}

func TestPalworldFixtureApplyReadRoundTripPreservesUnknownContent(t *testing.T) {
	root := t.TempDir()
	original := palworldFixtureData(t)
	target := writePalworldTarget(t, root, original)
	definition := palworldAdapterFixture()
	if err := Apply(root, definition, palworldValues()); err != nil {
		t.Fatal(err)
	}
	values, err := Read(root, definition)
	if err != nil {
		t.Fatal(err)
	}
	for key, expected := range palworldValues() {
		if values[key] != expected {
			t.Fatalf("%s=%q, want %q", key, values[key], expected)
		}
	}
	updated, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, preserved := range []string{"Difficulty=None", "DayTimeSpeedRate=1.000000", "CrossplayPlatforms=(Steam,Xbox,PS5,Mac)", "UnknownFutureSetting=-12", "[/Script/Pal.OtherSection]", "PreserveMe=True"} {
		if !strings.Contains(string(updated), preserved) {
			t.Fatalf("unknown content %q was lost: %s", preserved, updated)
		}
	}
	backup, err := os.ReadFile(filepath.Join(root, ".gamenode-backups", filepath.FromSlash(palworldTarget+".previous")))
	if err != nil || string(backup) != string(original) {
		t.Fatalf("backup mismatch: %v", err)
	}
}

func TestPalworldSeedCreatesMissingTargetAndParents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, palworldSeedFrom), palworldFixtureData(t), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, palworldAdapterFixture(), palworldValues()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(palworldTarget))
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".gamenode-backups", filepath.FromSlash(palworldTarget+".previous"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new target unexpectedly produced a previous-file backup: %v", err)
	}
}

func TestPalworldExistingTargetIsPatchedInsteadOfReseeded(t *testing.T) {
	root := t.TempDir()
	seed := strings.Replace(string(palworldFixtureData(t)), "UnknownFutureSetting=-12", "SeedOnly=True,UnknownFutureSetting=-12", 1)
	if err := os.WriteFile(filepath.Join(root, palworldSeedFrom), []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	targetData := strings.Replace(string(palworldFixtureData(t)), "UnknownFutureSetting=-12", "TargetOnly=True,UnknownFutureSetting=-12", 1)
	target := writePalworldTarget(t, root, []byte(targetData))
	if err := Apply(root, palworldAdapterFixture(), map[string]string{"SERVER_NAME": "Existing target"}); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(target)
	if !strings.Contains(string(updated), "TargetOnly=True") || strings.Contains(string(updated), "SeedOnly=True") {
		t.Fatalf("existing target was replaced from seed: %s", updated)
	}
}

func TestPalworldSeedFailuresDoNotCreatePartialTarget(t *testing.T) {
	for name, prepare := range map[string]func(*testing.T, string){
		"missing source": func(*testing.T, string) {},
		"malformed source": func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, palworldSeedFrom), []byte("[/Script/Pal.PalGameWorldSettings]\nOptionSettings=(ServerName=\"unterminated)"), 0600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			prepare(t, root)
			err := Apply(root, palworldAdapterFixture(), map[string]string{"SERVER_NAME": "safe"})
			if err == nil {
				t.Fatal("invalid seed accepted")
			}
			if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(palworldTarget))); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial target exists: %v", statErr)
			}
		})
	}
}

func TestPalworldDefinitionAndSymlinkEscapesAreRejected(t *testing.T) {
	for _, target := range []string{"../PalWorldSettings.ini", "C:/PalWorldSettings.ini", "//server/share/config.ini", "/etc/config.ini"} {
		definition := palworldAdapterFixture()
		definition.Target = target
		if err := ValidateDefinition(definition); err == nil {
			t.Fatalf("unsafe target accepted: %q", target)
		}
	}
	definition := palworldAdapterFixture()
	definition.Initialization.Source = "../other.ini"
	if err := ValidateDefinition(definition); err == nil {
		t.Fatal("unsafe seed accepted")
	}

	for name, linkTarget := range map[string]string{"source": palworldSeedFrom, "target": palworldTarget} {
		t.Run(name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			outsideFile := filepath.Join(outside, "outside.ini")
			if err := os.WriteFile(outsideFile, palworldFixtureData(t), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, filepath.FromSlash(linkTarget))
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outsideFile, link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			if name == "target" {
				if err := os.WriteFile(filepath.Join(root, palworldSeedFrom), palworldFixtureData(t), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Apply(root, palworldAdapterFixture(), map[string]string{"SERVER_NAME": "safe"}); err == nil {
				t.Fatal("symlink escape accepted")
			}
		})
	}
}

func TestSectionTupleDefinitionRejectsUnsafeDeclarativeParameters(t *testing.T) {
	mutations := map[string]func(*templates.ConfigAdapterDefinition){
		"unknown format":       func(definition *templates.ConfigAdapterDefinition) { definition.Format = "palworld-option-settings" },
		"section injection":    func(definition *templates.ConfigAdapterDefinition) { definition.Section = "Server]\nInjected=[x" },
		"container expression": func(definition *templates.ConfigAdapterDefinition) { definition.ContainerProperty = "Settings()" },
		"unknown init mode":    func(definition *templates.ConfigAdapterDefinition) { definition.Initialization.Mode = "copy-anywhere" },
		"absolute seed": func(definition *templates.ConfigAdapterDefinition) {
			definition.Initialization.Source = `C:/Windows/win.ini`
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			definition := palworldAdapterFixture()
			mutate(&definition)
			if err := ValidateDefinition(definition); err == nil {
				t.Fatal("unsafe descriptor accepted")
			}
		})
	}
}

func TestPalworldApplyWriteFailureLeavesExistingTarget(t *testing.T) {
	root := t.TempDir()
	original := palworldFixtureData(t)
	target := writePalworldTarget(t, root, original)
	err := applyWithWriter(root, palworldAdapterFixture(), map[string]string{"SERVER_NAME": "changed"}, func(string, []byte) error {
		return errors.New("injected write failure")
	})
	if !errors.Is(err, ErrApply) {
		t.Fatalf("error=%v", err)
	}
	after, _ := os.ReadFile(target)
	if string(after) != string(original) {
		t.Fatal("target changed after atomic writer failure")
	}
}

func TestSeedInitializationDoesNotReplaceConcurrentTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, palworldSeedFrom), palworldFixtureData(t), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(palworldTarget))
	err := applyWithWriters(root, palworldAdapterFixture(), map[string]string{"SERVER_NAME": "seeded"}, atomicWrite, func(name string, data []byte) error {
		if err := os.WriteFile(name, []byte("concurrent owner"), 0600); err != nil {
			return err
		}
		return atomicCreate(name, data)
	})
	if !errors.Is(err, ErrApply) {
		t.Fatalf("error=%v", err)
	}
	after, readErr := os.ReadFile(target)
	if readErr != nil || string(after) != "concurrent owner" {
		t.Fatalf("concurrent target was replaced: %q %v", after, readErr)
	}
}

func TestPalworldParserRejectsMalformedAmbiguousAndUnsupportedEncoding(t *testing.T) {
	fields := []templates.ConfigAdapterField{{Key: "NAME", Label: "Name", Type: "string", Property: "ServerName"}}
	for name, tuple := range map[string]string{
		"unterminated quote": `(ServerName="broken)`,
		"unbalanced nesting": `(ServerName="ok",Unknown=(A,B)`,
		"duplicate":          `(ServerName="one",ServerName="two")`,
		"newline":            "(ServerName=\"one\nInjected=True\")",
		"nul":                "(ServerName=\"one\x00two\")",
	} {
		t.Run(name, func(t *testing.T) {
			data := []byte("[/Script/Pal.PalGameWorldSettings]\nOptionSettings=" + tuple)
			if _, _, err := transformSectionTuple(data, nil, fields, "/Script/Pal.PalGameWorldSettings", "OptionSettings"); err == nil {
				t.Fatal("malformed tuple accepted")
			}
		})
	}
	if _, _, err := transformSectionTuple([]byte{0xff, 0xfe}, nil, fields, "/Script/Pal.PalGameWorldSettings", "OptionSettings"); err == nil {
		t.Fatal("UTF-16/invalid UTF-8 accepted")
	}
	huge := "[/Script/Pal.PalGameWorldSettings]\nOptionSettings=(ServerName=\"" + strings.Repeat("x", maxSectionTupleBytes) + "\")"
	if _, _, err := transformSectionTuple([]byte(huge), nil, fields, "/Script/Pal.PalGameWorldSettings", "OptionSettings"); err == nil {
		t.Fatal("oversized tuple accepted")
	}
}

func FuzzSectionTupleKeyValuesParser(f *testing.F) {
	f.Add(palworldFixtureData(f))
	f.Add([]byte("[Server]\nSettings=(Name=\"x\")"))
	f.Fuzz(func(t *testing.T, data []byte) {
		fields := []templates.ConfigAdapterField{{Key: "NAME", Label: "Name", Type: "string", Property: "Name"}}
		_, _, _ = transformSectionTuple(data, map[string]string{"Name": "safe"}, fields, "Server", "Settings")
	})
}

func TestSectionTupleFormatIsReusableAndTyped(t *testing.T) {
	tests := []struct {
		name, input, section, container string
		fields                          []templates.ConfigAdapterField
		replacements                    map[string]string
		want                            []string
	}{
		{
			name: "server settings", input: "[Server]\r\nSettings=(Name=\"Test\",Port=1234,Enabled=True)\r\n", section: "Server", container: "Settings",
			fields:       []templates.ConfigAdapterField{{Key: "NAME", Label: "Name", Type: "string", Property: "Name"}, {Key: "PORT", Label: "Port", Type: "integer", Property: "Port"}, {Key: "ENABLED", Label: "Enabled", Type: "boolean", Property: "Enabled"}},
			replacements: map[string]string{"Name": "Changed", "Port": "2345", "Enabled": "false"}, want: []string{`Name="Changed"`, "Port=2345", "Enabled=False", "\r\n"},
		},
		{
			name: "game options", input: "[Game]\nOptions=(Foo=\"bar\",Count=2,Enabled=False,Opaque=(A,B))\n", section: "Game", container: "Options",
			fields:       []templates.ConfigAdapterField{{Key: "FOO", Label: "Foo", Type: "string", Property: "Foo"}, {Key: "COUNT", Label: "Count", Type: "integer", Property: "Count"}, {Key: "ENABLED", Label: "Enabled", Type: "boolean", Property: "Enabled"}},
			replacements: map[string]string{"Foo": "baz"}, want: []string{`Foo="baz"`, "Count=2", "Enabled=False", "Opaque=(A,B)"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updated, values, err := transformSectionTuple([]byte(test.input), test.replacements, test.fields, test.section, test.container)
			if err != nil {
				t.Fatal(err)
			}
			if len(values) != len(test.fields) {
				t.Fatalf("values=%v", values)
			}
			for _, expected := range test.want {
				if !strings.Contains(string(updated), expected) {
					t.Fatalf("missing %q in %q", expected, updated)
				}
			}
		})
	}
}
