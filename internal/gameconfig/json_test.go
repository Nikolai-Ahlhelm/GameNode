package gameconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"gamenode/internal/templates"
)

func TestTransformJSONUpdatesDeclaredTopLevelScalars(t *testing.T) {
	input := []byte(`{"name":"Old","slotCount":16,"nested":{"keep":true},"items":[1,2]}`)
	updated, values, err := transformJSON(input, map[string]string{"name": "New", "slotCount": "8"}, map[string]bool{"name": true, "slotCount": true})
	if err != nil {
		t.Fatal(err)
	}
	if values["name"] != "New" || values["slotCount"] != "8" {
		t.Fatalf("unexpected values: %#v", values)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(updated, &decoded); err != nil {
		t.Fatal(err)
	}
	var nested map[string]bool
	if err := json.Unmarshal(decoded["nested"], &nested); err != nil {
		t.Fatal(err)
	}
	if string(decoded["name"]) != `"New"` || string(decoded["slotCount"]) != "8" || !nested["keep"] {
		t.Fatalf("unexpected JSON: %s", updated)
	}
}

func TestTransformJSONRejectsManagedObjectsAndArrays(t *testing.T) {
	for _, input := range []string{`{"settings":{"name":"x"}}`, `{"items":[1,2]}`} {
		if _, _, err := transformJSON([]byte(input), nil, map[string]bool{"settings": true}); err == nil {
			t.Fatalf("object/array was accepted: %s", input)
		}
	}
}

func TestJSONAdapterDefinitionUsesJSONTarget(t *testing.T) {
	definition := templates.ConfigAdapterDefinition{SchemaVersion: 1, ID: "json", Version: "1.0.0", Format: templates.FormatJSONKeyValues, Target: "settings.json", PostStartOnly: true, Fields: []templates.ConfigAdapterField{{Key: "NAME", Label: "Name", Type: "string", Property: "name", Validation: templates.Validation{}}}}
	if err := ValidateDefinition(definition); err != nil {
		t.Fatal(err)
	}
}

func TestTransformINISectionUpdatesOnlyDeclaredSection(t *testing.T) {
	input := []byte("; keep\r\n[Other]\r\nServerPassword=other\r\n[ServerSettings]\r\nServerPassword=old\r\nRCONPort=27020\r\nUnknown=keep\r\n")
	updated, values, err := transformINISection(input, map[string]string{"ServerPassword": "new", "RCONPort": "28020"}, map[string]bool{"ServerPassword": true, "RCONPort": true}, "ServerSettings")
	if err != nil {
		t.Fatal(err)
	}
	if values["ServerPassword"] != "new" || values["RCONPort"] != "28020" {
		t.Fatalf("unexpected values: %#v", values)
	}
	text := string(updated)
	if !strings.Contains(text, "[Other]\r\nServerPassword=other") || !strings.Contains(text, "[ServerSettings]\r\nServerPassword=new\r\nRCONPort=28020\r\nUnknown=keep") {
		t.Fatalf("unexpected INI: %q", text)
	}
}

func TestTransformINISectionRejectsMissingSection(t *testing.T) {
	if _, _, err := transformINISection([]byte("[Other]\nValue=1\n"), nil, map[string]bool{"Value": true}, "ServerSettings"); err == nil {
		t.Fatal("missing section was accepted")
	}
}
