package templates

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"gamenode"
	"gamenode/internal/database"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/7-days-to-die.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSevenDaysGoldenEgg(t *testing.T) {
	template, err := AnalyzeEgg(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if template.Name != "7 Days To Die" || template.Installer.Type != InstallerSteamCMD || template.Installer.SteamCMD.AppID != 294420 {
		t.Fatalf("unexpected normalized template: %#v", template)
	}
	if template.Installer.SteamCMD.InstallTarget != "server_root" || strings.Contains(template.Installer.SteamCMD.InstallTarget, "/mnt/server") {
		t.Fatal("container path escaped into plan")
	}
	if template.Launch == nil || template.Launch.Executable != "./7DaysToDieServer.x86_64" || len(template.Launch.Arguments) < 5 {
		t.Fatalf("launch = %#v", template.Launch)
	}
	if template.Compatibility.Status != PartiallyCompatible {
		t.Fatalf("status = %s", template.Compatibility.Status)
	}
	assertFinding(t, template, "SUPPORTED_STEAMCMD_INSTALL")
	assertFinding(t, template, "UNSUPPORTED_SHELL_STARTUP")
	assertFinding(t, template, "UNSUPPORTED_INSTALL_SCRIPT")
	vars := map[string]TemplateVariable{}
	for _, v := range template.Variables {
		vars[v.Key] = v
	}
	if vars["GAME_DIFFICULTY"].Type != "integer" || *vars["GAME_DIFFICULTY"].Validation.Min != 0 || *vars["GAME_DIFFICULTY"].Validation.Max != 5 {
		t.Fatal("integer bounds not imported")
	}
	if vars["AUTO_UPDATE"].Type != "boolean" || !vars["PASSWORD"].Sensitive || !vars["SRCDS_BETAPASS"].Sensitive {
		t.Fatal("variable typing or sensitivity failed")
	}
	if !vars["SERVER_PORT"].Required || vars["SERVER_PORT"].Type != "integer" || *vars["SERVER_PORT"].Validation.Max != 65535 || vars["MAX_PLAYERS"].Validation.MaxLength == nil {
		t.Fatal("runtime port or string length validation was not normalized")
	}
}

func TestEggInputSecurityLimits(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		contains string
	}{
		{"malformed", []byte(`{"name":`), "invalid egg JSON"},
		{"missing name", []byte(`{"variables":[]}`), "name is required"},
		{"duplicate", []byte(`{"name":"x","variables":[{"env_variable":"A"},{"env_variable":"a"}]}`), "duplicate"},
		{"bad key", []byte(`{"name":"x","variables":[{"env_variable":"../PATH"}]}`), "invalid egg variable"},
		{"too deep", []byte(`{"name":"x","x":[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[0]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]} `), "nesting"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := AnalyzeEgg(test.data)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	oversized := make([]byte, MaxEggBytes+1)
	if _, err := AnalyzeEgg(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error = %v", err)
	}
	variables := make([]string, MaxVariables+1)
	for i := range variables {
		variables[i] = `{"env_variable":"V` + string(rune(65+i%26)) + `"}`
	}
	_, err := AnalyzeEgg([]byte(`{"name":"x","variables":[` + strings.Join(variables, ",") + `]}`))
	if err == nil {
		t.Fatal("variable limit accepted")
	}
}

func TestValidationWarningsAndUnknownFields(t *testing.T) {
	template, err := AnalyzeEgg([]byte(`{"name":"x","future":{"x":1},"startup":"./server --value=${VALUE}","scripts":{"installation":{"script":"steamcmd +app_update ${SRCDS_APPID}"}},"variables":[{"name":"id","env_variable":"SRCDS_APPID","default_value":"10","rules":"required|string"},{"name":"value","env_variable":"VALUE","default_value":"x","rules":"string|regex:evil"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	assertFinding(t, template, "UNKNOWN_EGG_FIELD")
	assertFinding(t, template, "UNKNOWN_VALIDATION_RULE")
}

func TestSensitiveDefaultsAreDiscarded(t *testing.T) {
	template, err := AnalyzeEgg([]byte(`{"name":"x","startup":"./server","scripts":{"installation":{"script":"steamcmd +app_update ${SRCDS_APPID}"}},"variables":[{"name":"id","env_variable":"SRCDS_APPID","default_value":"10","rules":"integer"},{"name":"token","env_variable":"API_TOKEN","default_value":"must-not-persist","rules":"string|max:100"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if template.Variables[1].DefaultValue != "" || !template.Variables[1].Sensitive {
		t.Fatal("sensitive default retained")
	}
	assertFinding(t, template, "SENSITIVE_DEFAULT_REMOVED")
}

func TestSteamCMDDetectionMatrix(t *testing.T) {
	base := func(appID, extras string) []byte {
		return []byte(`{"name":"x","startup":"./server","scripts":{"installation":{"script":"steamcmd +force_install_dir /mnt/server +login anonymous +app_update ${SRCDS_APPID} ` + extras + ` +quit"}},"variables":[{"name":"id","env_variable":"SRCDS_APPID","default_value":"` + appID + `","rules":"required|integer"},{"name":"beta","env_variable":"SRCDS_BETAID","default_value":"","rules":"nullable|string"},{"name":"beta password","env_variable":"SRCDS_BETAPASS","default_value":"","rules":"nullable|string"}]}`)
	}
	valid, err := AnalyzeEgg(base("294420", "validate"))
	if err != nil {
		t.Fatal(err)
	}
	if valid.Installer.Type != InstallerSteamCMD || !valid.Installer.SteamCMD.Validate || valid.Installer.SteamCMD.LoginMode != "anonymous" || valid.Installer.SteamCMD.BetaBranchVariable != "SRCDS_BETAID" || valid.Installer.SteamCMD.BetaPasswordVariable != "SRCDS_BETAPASS" {
		t.Fatalf("plan=%#v", valid.Installer)
	}
	missing, err := AnalyzeEgg([]byte(`{"name":"x","startup":"./server","scripts":{"installation":{"script":"steamcmd +app_update 10"}},"variables":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if missing.Installer.Type != InstallerUnsupported {
		t.Fatal("missing AppID accepted")
	}
	assertFinding(t, missing, "STEAMCMD_APP_ID_MISSING")
	if _, err = AnalyzeEgg(base("not-a-number", "")); err == nil {
		t.Fatal("malformed typed AppID accepted")
	}
	unsupported, err := AnalyzeEgg([]byte(`{"name":"x","startup":"./server","scripts":{"installation":{"script":"curl https://example.invalid/game"}},"variables":[{"env_variable":"SRCDS_APPID","default_value":"10","rules":"integer"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.Installer.Type != InstallerUnsupported {
		t.Fatal("unsupported installer accepted")
	}
}

func TestControlledExpansion(t *testing.T) {
	known := map[string]bool{"PORT": true}
	value, err := Expand("-port=${PORT}", map[string]string{"PORT": "8080;touch nope"}, known)
	if err != nil || value != "-port=8080;touch nope" {
		t.Fatalf("expand = %q, %v", value, err)
	}
	if _, err = Expand("${PATH}", nil, known); err == nil {
		t.Fatal("host variable accepted")
	}
	if _, err = Expand("$(whoami)", nil, known); err == nil {
		t.Fatal("unsupported expansion accepted")
	}
	if path, err := ExpandRelativePath("configs/${PORT}.xml", map[string]string{"PORT": "8080"}, known); err != nil || path != "configs/8080.xml" {
		t.Fatalf("relative path = %q, %v", path, err)
	}
	if _, err := ExpandRelativePath("${PORT}", map[string]string{"PORT": "../../outside"}, known); err == nil {
		t.Fatal("traversal expansion accepted")
	}
	if _, err := ExpandRelativePath("${PORT}", map[string]string{"PORT": "C:\\outside"}, known); err == nil {
		t.Fatal("absolute expansion accepted")
	}
}

func TestStoreRoundTripAndCascade(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewStore(db))
	created, err := service.Import(context.Background(), fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := service.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != created.Name || len(loaded.Variables) != len(created.Variables) || len(loaded.Compatibility.Findings) != len(created.Compatibility.Findings) {
		t.Fatal("round trip changed normalized template")
	}
	if err = service.Delete(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM game_template_variables`).Scan(&count); err != nil || count != 0 {
		t.Fatal("child rows not cascaded")
	}
}

func TestBuiltinTemplatesOverlayAndReadOnly(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewStore(db))
	items, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "builtin-minecraft-neoforge" || !items[0].ReadOnly || items[0].Launch.Resolver != "neoforge" {
		t.Fatalf("built-in overlay = %#v", items)
	}
	if err = service.Delete(context.Background(), items[0].ID); !errors.Is(err, ErrBuiltinReadOnly) {
		t.Fatalf("built-in delete error = %v", err)
	}
}

func assertFinding(t *testing.T, template Template, code string) {
	t.Helper()
	for _, finding := range template.Compatibility.Findings {
		if finding.Code == code {
			return
		}
	}
	t.Fatalf("missing finding %s", code)
}
