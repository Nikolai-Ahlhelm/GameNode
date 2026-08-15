package templates

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bindingTemplate provides the variable definitions a managed-launch adapter
// must agree with. The base launch deliberately references only SERVER_PORT.
func bindingTemplate() Template {
	minLength, maxLength := 1, 64
	return Template{
		SchemaVersion:    2,
		ID:               "binding-test",
		Version:          "1.0.0",
		PlatformLaunches: map[string]LaunchDefinition{"windows": {Executable: "game.exe", Arguments: []string{"-port", "{{SERVER_PORT}}"}, WorkingRoot: "server_root"}},
		Variables: []TemplateVariable{
			{Key: "SERVER_NAME", Type: "string", DefaultValue: "Test", Required: true, Validation: Validation{MinLength: &minLength, MaxLength: &maxLength}},
			{Key: "SERVER_PASSWORD", Type: "secret", Sensitive: true, Nullable: true, Validation: Validation{MaxLength: &maxLength}},
			{Key: "PUBLIC", Type: "boolean", DefaultValue: "0", Required: true},
			{Key: "SERVER_PORT", Type: "integer", DefaultValue: "2456", Required: true},
		},
	}
}

func decodeBinding(t *testing.T, body string, version int) (ConfigAdapterDefinition, error) {
	t.Helper()
	reference := ConfigAdapterReference{ID: "settings", SchemaVersion: version, File: "settings.adapter.json"}
	return decodeConfigAdapter([]byte(body), reference, bindingTemplate())
}

const managedLaunchAdapter = `{"schema_version":2,"id":"settings","version":"1.0.0","format":"managed-launch","restart_required":true,"fields":[
 {"key":"SERVER_NAME","label":"Server name","section":"General","type":"string","required":true,"nullable":false,"sensitive":false,"validation":{"min_length":1,"max_length":64},"binding":{"type":"launch-value","argument":"-name"}},
 {"key":"SERVER_PASSWORD","label":"Password","section":"Security","type":"secret","required":false,"nullable":true,"sensitive":true,"validation":{"max_length":64},"binding":{"type":"launch-secret","argument":"-password"}},
 {"key":"PUBLIC","label":"Public","section":"Networking","type":"boolean","required":true,"nullable":false,"sensitive":false,"validation":{},"binding":{"type":"launch-value","argument":"-public","true_value":"1","false_value":"0"}}]}`

// TestAdapterSchemaV1RemainsReadable protects the existing file adapters from
// the schema v2 extension.
func TestAdapterSchemaV1RemainsReadable(t *testing.T) {
	body := `{"schema_version":1,"id":"settings","version":"1.0.0","format":"ini-key-values","target":"server.ini","restart_required":true,"fields":[{"key":"SERVER_NAME","label":"Server name","type":"string","property":"ServerName","required":true,"nullable":false,"sensitive":false,"validation":{"min_length":1,"max_length":64}}]}`
	adapter, err := decodeBinding(t, body, 1)
	if err != nil {
		t.Fatalf("v1 adapter must remain readable: %v", err)
	}
	if adapter.Fields[0].Property != "ServerName" || adapter.Fields[0].Binding != nil {
		t.Fatalf("unexpected v1 field: %#v", adapter.Fields[0])
	}
}

func TestManagedLaunchAdapterDecodes(t *testing.T) {
	adapter, err := decodeBinding(t, managedLaunchAdapter, 2)
	if err != nil {
		t.Fatalf("managed launch adapter: %v", err)
	}
	if adapter.Format != FormatManagedLaunch || adapter.Target != "" || len(adapter.Fields) != 3 {
		t.Fatalf("unexpected adapter: %#v", adapter)
	}
	if adapter.Fields[0].Binding.Type != BindingLaunchValue || adapter.Fields[0].Binding.Argument != "-name" {
		t.Fatalf("unexpected binding: %#v", adapter.Fields[0].Binding)
	}
	if !adapter.Fields[1].Binding.SecretBinding() || !adapter.Fields[2].Binding.LaunchBinding() {
		t.Fatalf("binding classification is wrong: %#v", adapter.Fields)
	}
}

// TestManagedLaunchAdapterRejections locks the closed binding whitelist. Each
// case must fail closed rather than silently producing a launch.
func TestManagedLaunchAdapterRejections(t *testing.T) {
	cases := map[string]string{
		"unknown binding type":          `"binding":{"type":"launch-script","argument":"-name"}`,
		"unknown binding field":         `"binding":{"type":"launch-value","argument":"-name","shell":true}`,
		"missing binding":               `"required":true`,
		"argument with space":           `"binding":{"type":"launch-value","argument":"-name x"}`,
		"argument with shell operator":  `"binding":{"type":"launch-value","argument":"-name;rm"}`,
		"argument without dash":         `"binding":{"type":"launch-value","argument":"name"}`,
		"launch binding with env name":  `"binding":{"type":"launch-value","argument":"-name","name":"NAME"}`,
		"environment binding with arg":  `"binding":{"type":"environment-value","argument":"-name","name":"NAME"}`,
		"invalid environment name":      `"binding":{"type":"environment-value","name":"bad name"}`,
		"lowercase environment name":    `"binding":{"type":"environment-value","name":"lower"}`,
		"secret binding on plain field": `"binding":{"type":"launch-secret","argument":"-name"}`,
		"flag on string field":          `"binding":{"type":"launch-flag","argument":"-name"}`,
		"mapping on string field":       `"binding":{"type":"launch-value","argument":"-name","true_value":"1","false_value":"0"}`,
		"half mapping":                  `"binding":{"type":"launch-value","argument":"-name","true_value":"1"}`,
	}
	for name, binding := range cases {
		body := `{"schema_version":2,"id":"settings","version":"1.0.0","format":"managed-launch","restart_required":true,"fields":[{"key":"SERVER_NAME","label":"Server name","type":"string","required":true,"nullable":false,"sensitive":false,"validation":{"min_length":1,"max_length":64},` + binding + `}]}`
		if _, err := decodeBinding(t, body, 2); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

func TestManagedLaunchAdapterShapeRejections(t *testing.T) {
	field := `{"key":"SERVER_NAME","label":"Server name","type":"string","required":true,"nullable":false,"sensitive":false,"validation":{"min_length":1,"max_length":64},"binding":{"type":"launch-value","argument":"-name"}}`
	cases := map[string]struct {
		body    string
		version int
	}{
		"managed launch requires schema v2": {`{"schema_version":1,"id":"settings","version":"1.0.0","format":"managed-launch","restart_required":true,"fields":[` + field + `]}`, 1},
		"managed launch rejects a target":   {`{"schema_version":2,"id":"settings","version":"1.0.0","format":"managed-launch","target":"server.ini","restart_required":true,"fields":[` + field + `]}`, 2},
		"managed launch rejects seeding":    {`{"schema_version":2,"id":"settings","version":"1.0.0","format":"managed-launch","initialization":{"mode":"seed-from-file","source":"default.ini"},"restart_required":true,"fields":[` + field + `]}`, 2},
		"managed launch rejects post start": {`{"schema_version":2,"id":"settings","version":"1.0.0","format":"managed-launch","post_start_only":true,"restart_required":true,"fields":[` + field + `]}`, 2},
		"binding and property together":     {`{"schema_version":2,"id":"settings","version":"1.0.0","format":"managed-launch","restart_required":true,"fields":[{"key":"SERVER_NAME","label":"Server name","type":"string","property":"ServerName","required":true,"nullable":false,"sensitive":false,"validation":{"min_length":1,"max_length":64},"binding":{"type":"launch-value","argument":"-name"}}]}`, 2},
		"binding on a file adapter":         {`{"schema_version":2,"id":"settings","version":"1.0.0","format":"ini-key-values","target":"server.ini","restart_required":true,"fields":[{"key":"SERVER_NAME","label":"Server name","type":"string","property":"ServerName","required":true,"nullable":false,"sensitive":false,"validation":{"min_length":1,"max_length":64},"binding":{"type":"launch-value","argument":"-name"}}]}`, 2},
		"duplicate argument":                {`{"schema_version":2,"id":"settings","version":"1.0.0","format":"managed-launch","restart_required":true,"fields":[` + field + `,{"key":"PUBLIC","label":"Public","type":"boolean","required":true,"nullable":false,"sensitive":false,"validation":{},"binding":{"type":"launch-flag","argument":"-name"}}]}`, 2},
	}
	for name, item := range cases {
		if _, err := decodeBinding(t, item.body, item.version); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// TestManagedLaunchRejectsBaseLaunchCollision keeps one source of truth: a
// setting bound to the launch must not also be expanded by the base launch.
func TestManagedLaunchRejectsBaseLaunchCollision(t *testing.T) {
	template := bindingTemplate()
	template.PlatformLaunches["windows"] = LaunchDefinition{Executable: "game.exe", Arguments: []string{"-name", "{{SERVER_NAME}}"}, WorkingRoot: "server_root"}
	reference := ConfigAdapterReference{ID: "settings", SchemaVersion: 2, File: "settings.adapter.json"}
	if _, err := decodeConfigAdapter([]byte(managedLaunchAdapter), reference, template); err == nil {
		t.Fatal("a base launch placeholder for a managed key must be rejected")
	}
}

// TestValheimOfficialAdapterBindings pins the reference implementation.
func TestValheimOfficialAdapterBindings(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "valheim", "valheim-settings.adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var adapter ConfigAdapterDefinition
	if err = json.Unmarshal(data, &adapter); err != nil {
		t.Fatal(err)
	}
	wanted := map[string]string{"SERVER_NAME": BindingLaunchValue, "WORLD_NAME": BindingLaunchValue, "SERVER_PASSWORD": BindingLaunchSecret, "PUBLIC_VISIBILITY": BindingLaunchValue, "CROSSPLAY": BindingLaunchFlag, "SAVE_INTERVAL_SECONDS": BindingLaunchValue}
	if adapter.Format != FormatManagedLaunch || adapter.SchemaVersion != AdapterSchemaVersion || len(adapter.Fields) != len(wanted) {
		t.Fatalf("unexpected Valheim adapter: %#v", adapter)
	}
	for _, field := range adapter.Fields {
		if wanted[field.Key] != field.Binding.Type {
			t.Fatalf("unexpected binding for %s: %#v", field.Key, field.Binding)
		}
		if field.Binding.Argument == "" || !strings.HasPrefix(field.Binding.Argument, "-") {
			t.Fatalf("unexpected argument for %s: %q", field.Key, field.Binding.Argument)
		}
	}
}
