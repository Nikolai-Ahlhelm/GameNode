package gameconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/templates"
)

func adapterFixture() templates.ConfigAdapterDefinition {
	minPort, maxPort, maxLength := float64(1024), float64(65535), 128
	return templates.ConfigAdapterDefinition{SchemaVersion: 1, ID: "serverconfig", Version: "1.0.0", Format: "xml-properties", Target: "serverconfig.xml", RestartRequired: true, Fields: []templates.ConfigAdapterField{
		{Key: "NAME", Label: "Server name", Type: "string", Property: "ServerName", Required: true, Validation: templates.Validation{MaxLength: &maxLength}},
		{Key: "PORT", Label: "Port", Type: "integer", Property: "ServerPort", Required: true, Validation: templates.Validation{Min: &minPort, Max: &maxPort}},
		{Key: "PASSWORD", Label: "Password", Type: "secret", Property: "ServerPassword", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}},
	}}
}

func writeFixture(t *testing.T, root, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "serverconfig.xml"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestXMLPropertiesApplyReadEscapeAndBackup(t *testing.T) {
	root := t.TempDir()
	original := `<?xml version="1.0"?><ServerSettings><!-- preserved --><property name="ServerName" value="Old"/><property name="ServerPort" value="26900"/><property name="ServerPassword" value="old-secret"/><property name="Unknown" value="keep"/></ServerSettings>`
	writeFixture(t, root, original)
	definition := adapterFixture()
	if err := Apply(root, definition, map[string]string{"NAME": `A & B "quoted"`, "PORT": "26901", "PASSWORD": "new<secret"}); err != nil {
		t.Fatal(err)
	}
	values, err := Read(root, definition)
	if err != nil {
		t.Fatal(err)
	}
	if values["NAME"] != `A & B "quoted"` || values["PORT"] != "26901" || values["PASSWORD"] != "new<secret" {
		t.Fatalf("values=%#v", values)
	}
	updated, _ := os.ReadFile(filepath.Join(root, "serverconfig.xml"))
	if !strings.Contains(string(updated), `name="Unknown" value="keep"`) || !strings.Contains(string(updated), "<!-- preserved -->") {
		t.Fatalf("unknown content lost: %s", updated)
	}
	backup, err := os.ReadFile(filepath.Join(root, ".gamenode-backups", "serverconfig.xml.previous"))
	if err != nil || string(backup) != original {
		t.Fatalf("backup=%q err=%v", backup, err)
	}
}

func TestXMLPropertiesRejectsUnsafeDefinitionsAndValues(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, `<ServerSettings><property name="ServerName" value="Old"/><property name="ServerPort" value="26900"/><property name="ServerPassword" value=""/></ServerSettings>`)
	for name, mutate := range map[string]func(*templates.ConfigAdapterDefinition){
		"target traversal":   func(value *templates.ConfigAdapterDefinition) { value.Target = "../outside.xml" },
		"unknown format":     func(value *templates.ConfigAdapterDefinition) { value.Format = "xpath" },
		"arbitrary property": func(value *templates.ConfigAdapterDefinition) { value.Fields[0].Property = `Name']/../../evil` },
	} {
		t.Run(name, func(t *testing.T) {
			value := adapterFixture()
			mutate(&value)
			if err := Apply(root, value, map[string]string{"NAME": "safe"}); err == nil {
				t.Fatal("unsafe definition accepted")
			}
		})
	}
	for name, values := range map[string]map[string]string{"unknown field": {"APP_ID": "10"}, "invalid port": {"PORT": "1"}, "invalid integer": {"PORT": "x"}} {
		t.Run(name, func(t *testing.T) {
			if err := Apply(root, adapterFixture(), values); err == nil {
				t.Fatal("invalid value accepted")
			}
		})
	}
}

func TestXMLPropertiesRejectsDirectivesDuplicatesAndMissingProperties(t *testing.T) {
	for name, contents := range map[string]string{
		"directive": `<!DOCTYPE x><ServerSettings><property name="ServerName" value="x"/><property name="ServerPort" value="26900"/><property name="ServerPassword" value=""/></ServerSettings>`,
		"duplicate": `<ServerSettings><property name="ServerName" value="x"/><property name="ServerName" value="y"/><property name="ServerPort" value="26900"/><property name="ServerPassword" value=""/></ServerSettings>`,
		"missing":   `<ServerSettings><property name="ServerName" value="x"/><property name="ServerPort" value="26900"/></ServerSettings>`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, contents)
			if _, err := Read(root, adapterFixture()); err == nil {
				t.Fatal("unsafe XML accepted")
			}
		})
	}
}

func iniAdapterFixture() templates.ConfigAdapterDefinition {
	minPlayers, maxPlayers, maxLength := float64(1), float64(100), 64
	return templates.ConfigAdapterDefinition{SchemaVersion: 1, ID: "project-zomboid-server-ini", Version: "1.0.0", Format: "ini-key-values", Target: "Server/gamenode.ini", RestartRequired: true, PostStartOnly: true, Fields: []templates.ConfigAdapterField{
		{Key: "PZ_PUBLIC_NAME", Label: "Public name", Type: "string", Property: "PublicName", Required: true, Validation: templates.Validation{MinLength: intPointer(1), MaxLength: &maxLength}},
		{Key: "PZ_MAX_PLAYERS", Label: "Maximum players", Type: "integer", Property: "MaxPlayers", Required: true, Validation: templates.Validation{Min: &minPlayers, Max: &maxPlayers}},
		{Key: "PZ_PUBLIC", Label: "Public", Type: "boolean", Property: "Public", Required: true},
		{Key: "PZ_PASSWORD", Label: "Password", Type: "secret", Property: "Password", Nullable: true, Sensitive: true, Validation: templates.Validation{MaxLength: &maxLength}},
	}}
}

func intPointer(value int) *int { return &value }

func writeINI(t *testing.T, root, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "Server"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Server", "gamenode.ini"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestINIKeyValuesApplyReadPreservesLayoutUnknownKeysAndBackup(t *testing.T) {
	root := t.TempDir()
	original := "\ufeff# Project Zomboid\r\nPublicName=Old name\r\nMaxPlayers=32\r\nPublic=false\r\nPassword=old-secret\r\nUnknownSetting=keep=this\r\n\r\n"
	writeINI(t, root, original)
	definition := iniAdapterFixture()
	if err := Apply(root, definition, map[string]string{"PZ_PUBLIC_NAME": "GameNode PZ", "PZ_MAX_PLAYERS": "12", "PZ_PUBLIC": "true", "PZ_PASSWORD": "new=secret"}); err != nil {
		t.Fatal(err)
	}
	values, err := Read(root, definition)
	if err != nil {
		t.Fatal(err)
	}
	if values["PZ_PUBLIC_NAME"] != "GameNode PZ" || values["PZ_MAX_PLAYERS"] != "12" || values["PZ_PUBLIC"] != "true" || values["PZ_PASSWORD"] != "new=secret" {
		t.Fatalf("values=%#v", values)
	}
	updated, _ := os.ReadFile(filepath.Join(root, "Server", "gamenode.ini"))
	if !bytes.HasPrefix(updated, []byte("\xef\xbb\xbf")) || !strings.Contains(string(updated), "UnknownSetting=keep=this\r\n") || !strings.Contains(string(updated), "# Project Zomboid\r\n") {
		t.Fatalf("INI layout was not preserved: %q", updated)
	}
	backup, err := os.ReadFile(filepath.Join(root, ".gamenode-backups", "Server", "gamenode.ini.previous"))
	if err != nil || string(backup) != original {
		t.Fatalf("backup=%q err=%v", backup, err)
	}
}

func TestINIKeyValuesRejectsMalformedDuplicateMissingSectionsAndInjection(t *testing.T) {
	valid := "PublicName=Old\nMaxPlayers=32\nPublic=false\nPassword=\n"
	for name, contents := range map[string]string{
		"duplicate":   valid + "PublicName=Again\n",
		"missing":     "PublicName=Old\nMaxPlayers=32\nPublic=false\n",
		"section":     "[Server]\n" + valid,
		"malformed":   "not-a-setting\n" + valid,
		"invalid key": "../PublicName=Old\n" + valid,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeINI(t, root, contents)
			if _, err := Read(root, iniAdapterFixture()); err == nil {
				t.Fatal("unsafe INI accepted")
			}
		})
	}
	root := t.TempDir()
	writeINI(t, root, valid)
	if err := Apply(root, iniAdapterFixture(), map[string]string{"PZ_PUBLIC_NAME": "safe\nPublic=true"}); err == nil {
		t.Fatal("INI line injection accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "Server", "gamenode.ini"), []byte{0xff, 0xfe}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root, iniAdapterFixture()); err == nil {
		t.Fatal("invalid UTF-8 INI accepted")
	}
}

func TestINIKeyValuesRejectsUnsafeNestedTarget(t *testing.T) {
	for _, target := range []string{"../gamenode.ini", "Server/../../evil.ini", `Server\\gamenode.ini`, "Server/config.xml", "Server/deeper/than/the/limit/gamenode.ini"} {
		definition := iniAdapterFixture()
		definition.Target = target
		if err := ValidateDefinition(definition); err == nil {
			t.Fatalf("unsafe INI target accepted: %q", target)
		}
	}
}
