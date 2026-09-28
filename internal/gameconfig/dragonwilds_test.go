package gameconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/templates"
)

func dragonwildsDir() string {
	return filepath.Join("..", "..", "templates", "steamcmd", "runescape-dragonwilds")
}

func dragonwildsAdapter(t *testing.T, platform string) templates.ConfigAdapterDefinition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dragonwildsDir(), "dragonwilds-"+platform+".adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var definition templates.ConfigAdapterDefinition
	if err = json.Unmarshal(data, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestDragonwildsAdaptersEditOnlyManagedKeysInGeneratedFile(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join(dragonwildsDir(), "fixtures", "DedicatedServer.example.ini"))
	if err != nil {
		t.Fatal(err)
	}
	// transformINISection deliberately preserves each line's own CRLF/LF
	// terminator (real Unreal Engine .ini files are commonly CRLF on Windows
	// servers), so which terminator this checked-out fixture has must not
	// change what this test asserts. Normalize it to LF here so the test is
	// deterministic regardless of the checkout platform's line-ending
	// handling, independent of the adapter/parser behavior under test.
	fixture = bytes.ReplaceAll(fixture, []byte("\r\n"), []byte("\n"))
	for platform, folder := range map[string]string{"windows": "WindowsServer", "linux": "LinuxServer"} {
		t.Run(platform, func(t *testing.T) {
			definition := dragonwildsAdapter(t, platform)
			if err := ValidateDefinition(definition); err != nil {
				t.Fatal(err)
			}
			if definition.Target != "RSDragonwilds/Saved/Config/"+folder+"/DedicatedServer.ini" || !templates.AdapterAppliesTo(definition, platform) {
				t.Fatalf("unexpected adapter target/platform: %#v", definition)
			}
			root := t.TempDir()
			target := filepath.Join(root, filepath.FromSlash(definition.Target))
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(target, fixture, 0600); err != nil {
				t.Fatal(err)
			}
			values := map[string]string{"OWNER_ID": "0123456789abcdef0123456789abcdef", "SERVER_NAME": "GameNode", "DEFAULT_WORLD_NAME": "Rune Valley", "ADMIN_PASSWORD": "s3cret", "WORLD_PASSWORD": ""}
			if err = Apply(root, definition, values); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			for _, want := range []string{"OwnerId=0123456789abcdef0123456789abcdef\n", "ServerName=GameNode\n", "DefaultWorldName=Rune Valley\n", "AdminPassword=s3cret\n", "WorldPassword=\n", "ServerGuid=0123456789ABCDEF0123456789ABCDEF", "[SectionsToSave]\nbCanSaveAllSections=true"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in:\n%s", want, text)
				}
			}
			if err = Apply(root, definition, map[string]string{"SERVER_NAME": "line\nbreak"}); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("newline value err=%v", err)
			}
			if err = Apply(root, definition, map[string]string{"OWNER_ID": ""}); !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("empty required owner err=%v", err)
			}
		})
	}
}

func TestAdapterPlatformsMustBeValid(t *testing.T) {
	definition := dragonwildsAdapter(t, "windows")
	for _, platforms := range [][]string{{"darwin"}, {"windows", "windows"}, {"windows", "linux", "linux"}, {""}} {
		definition.Platforms = platforms
		if err := ValidateDefinition(definition); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("platforms %v err=%v", platforms, err)
		}
	}
}
