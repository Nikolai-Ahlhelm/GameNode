package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gamenode "gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/provisioning"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

// newRepoTemplateAPI serves one real repository template through the official
// catalog path, so the handler under test sees exactly what users get.
func newRepoTemplateAPI(t *testing.T, id, file, installer string, platforms []string) (http.Handler, *sql.DB, testSession) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	templateData, err := os.ReadFile(filepath.Join("..", "..", "templates", filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	var template templates.Template
	if err = json.Unmarshal(templateData, &template); err != nil {
		t.Fatal(err)
	}
	entry := templates.CatalogEntry{ID: template.ID, Name: template.Name, Description: template.Description, Category: template.Category, Version: template.Version, TemplateSchemaVersion: template.SchemaVersion, Platforms: template.Platforms, Installer: installer, File: file, Tags: template.Tags, Icon: template.Icon, MinimumGameNode: template.MinimumGameNode}
	catalogData, _ := json.Marshal(templates.CatalogManifest{SchemaVersion: 1, Templates: []templates.CatalogEntry{entry}})
	catalog := templates.NewCatalogManager(apiCatalogSource{catalog: catalogData, template: templateData}, t.TempDir(), "99.0.0")
	if _, err = catalog.Refresh(context.Background()); err != nil {
		t.Fatalf("the repository template must load through the official catalog: %v", err)
	}
	templateService := templates.NewServiceWithCatalog(templates.NewStore(db), catalog)
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	provisioner := provisioning.NewWithOptions(db, templateService, &apiInstaller{}, serverService, t.TempDir(), provisioning.Options{})
	t.Cleanup(provisioner.Close)
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{Templates: templateService, Provisioning: provisioner}).Handler(http.NotFoundHandler())
	return handler, db, createAdminSession(t, handler)
}

func fakeJavaHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	name := "java"
	if runtime.GOOS == "windows" {
		name = "java.exe"
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bin", name), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JAVA_HOME", home)
}

func zipBytes(files map[string]string) []byte {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, _ := writer.Create(name)
		entry.Write([]byte(content))
	}
	writer.Close()
	return buffer.Bytes()
}

func uploadTo(handler http.Handler, session testSession, serverID, filename string, data []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", filename)
	part.Write(data)
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers/"+serverID+"/mods", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", session.csrf)
	request.AddCookie(session.cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestHytaleAdoptValidatesFolderAndUsesHytaleModProfile(t *testing.T) {
	fakeJavaHome(t)
	handler, _, admin := newRepoTemplateAPI(t, "hytale", "hytale/template.json", templates.InstallerExistingFiles, []string{"windows", "linux"})
	root := t.TempDir()
	adopt := func(action string, body map[string]any) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(body)
		return templateRequest(handler, http.MethodPost, "/api/v1/templates/hytale/"+action, payload, &admin, true)
	}
	base := map[string]any{"server_name": "Hytale test", "server_root": root, "variables": map[string]string{"SERVER_PORT": "5530", "MAX_MEMORY_MB": "6144"}}
	if response := adopt("resolve", base); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an empty folder must be rejected: %d %s", response.Code, response.Body.String())
	}
	if response := templateRequest(handler, http.MethodPost, "/api/v1/templates/hytale/adopt", []byte(`{}`), nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated adopt: %d", response.Code)
	}
	for name, content := range map[string]string{"Server/HytaleServer.jar": "jar", "Assets.zip": "zip"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	response := adopt("resolve", base)
	var preview struct {
		Arguments   []string `json:"arguments"`
		StopCommand string   `json:"stop_command"`
		Ports       []string `json:"ports"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &preview) != nil {
		t.Fatalf("resolve: %d %s", response.Code, response.Body.String())
	}
	want := "-Xms2048M -Xmx6144M -jar Server/HytaleServer.jar --assets Assets.zip --bind 0.0.0.0:5530"
	if strings.Join(preview.Arguments, " ") != want || preview.StopCommand != "/stop" || strings.Join(preview.Ports, ",") != "UDP 5530" {
		t.Fatalf("preview = %+v", preview)
	}
	if response = adopt("resolve", map[string]any{"server_name": "x", "server_root": root, "variables": map[string]string{"SERVER_PORT": "80"}}); response.Code != http.StatusBadRequest {
		t.Fatalf("an out-of-range port must be rejected: %d", response.Code)
	}
	if response = adopt("resolve", map[string]any{"server_name": "x", "server_root": root, "variables": map[string]string{"NOT_A_VARIABLE": "1"}}); response.Code != http.StatusBadRequest {
		t.Fatalf("an unknown variable must be rejected: %d", response.Code)
	}
	response = adopt("adopt", base)
	var record struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &record) != nil {
		t.Fatalf("adopt: %d %s", response.Code, response.Body.String())
	}
	// The adopted server is an ordinary native server and uses the Hytale mod
	// profile (.jar and .zip in <root>/mods), not the Minecraft one.
	list := templateRequest(handler, http.MethodGet, "/api/v1/servers/"+record.Server.ID+"/mods", nil, &admin, false)
	if !strings.Contains(list.Body.String(), `"game":"hytale"`) || !strings.Contains(list.Body.String(), `"available":true`) || !strings.Contains(list.Body.String(), `".zip"`) {
		t.Fatalf("mods profile: %s", list.Body.String())
	}
	manifest := `{"Group":"com.example","Name":"Hello","Version":"1.2.0","Description":"Greets players"}`
	if response = uploadTo(handler, admin, record.Server.ID, "hello.zip", zipBytes(map[string]string{"manifest.json": manifest})); response.Code != http.StatusCreated {
		t.Fatalf("zip pack: %d %s", response.Code, response.Body.String())
	}
	if response = uploadTo(handler, admin, record.Server.ID, "plugin.jar", zipBytes(map[string]string{"manifest.json": manifest})); response.Code != http.StatusCreated {
		t.Fatalf("jar plugin: %d %s", response.Code, response.Body.String())
	}
	if response = uploadTo(handler, admin, record.Server.ID, "tool.exe", []byte("MZ")); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported extension: %d", response.Code)
	}
	list = templateRequest(handler, http.MethodGet, "/api/v1/servers/"+record.Server.ID+"/mods", nil, &admin, false)
	if !strings.Contains(list.Body.String(), `"id":"com.example:Hello"`) || !strings.Contains(list.Body.String(), `"version":"1.2.0"`) {
		t.Fatalf("manifest metadata: %s", list.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "mods", "hello.zip")); err != nil {
		t.Fatalf("mods must be stored in <root>/mods: %v", err)
	}
}

func TestVintageStoryServersUseDataModsProfile(t *testing.T) {
	handler, db, admin := newRepoTemplateAPI(t, "vintage-story", "vintagestory/template.json", templates.InstallerVintageStory, []string{"windows", "linux"})
	root := t.TempDir()
	executable, _ := os.Executable()
	payload, _ := json.Marshal(map[string]any{"creation_mode": "custom", "name": "VS", "working_directory": root, "executable": executable, "arguments": []string{}, "environment_variables": map[string]string{}, "stop_timeout_seconds": 1})
	response := templateRequest(handler, http.MethodPost, "/api/v1/servers", payload, &admin, true)
	var record struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &record) != nil {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	// Give the server Vintage Story provenance exactly as provisioning records it.
	if _, err := db.Exec(`INSERT INTO server_template_variables(server_id,template_id,variable_key,sensitive,template_source,template_version) VALUES(?,?,?,?,?,?)`, record.Server.ID, "vintage-story", "VS_VERSION", false, "official", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	info := `{type:"code", modid:"carrycapacity", name:"Carry Capacity", version:"1.9.0",}`
	if response = uploadTo(handler, admin, record.Server.ID, "carrycapacity_1.9.0.zip", zipBytes(map[string]string{"modinfo.json": info})); response.Code != http.StatusCreated {
		t.Fatalf("zip mod: %d %s", response.Code, response.Body.String())
	}
	if response = uploadTo(handler, admin, record.Server.ID, "other.jar", zipBytes(nil)); response.Code != http.StatusBadRequest {
		t.Fatalf("only .zip mods: %d", response.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "Mods", "carrycapacity_1.9.0.zip")); err != nil {
		t.Fatalf("mods must live in <data>/Mods: %v", err)
	}
	list := templateRequest(handler, http.MethodGet, "/api/v1/servers/"+record.Server.ID+"/mods", nil, &admin, false)
	body := list.Body.String()
	if !strings.Contains(body, `"game":"vintagestory"`) || !strings.Contains(body, `"directory":"data/Mods"`) || !strings.Contains(body, `"id":"carrycapacity"`) || !strings.Contains(body, `"name":"Carry Capacity"`) {
		t.Fatalf("list: %s", body)
	}
}

func TestVintageStoryVersionsEndpointGuards(t *testing.T) {
	handler, _, admin := newRepoTemplateAPI(t, "vintage-story", "vintagestory/template.json", templates.InstallerVintageStory, []string{"windows", "linux"})
	if response := templateRequest(handler, http.MethodGet, "/api/v1/vintagestory/versions", nil, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", response.Code)
	}
	if response := templateRequest(handler, http.MethodPost, "/api/v1/vintagestory/versions", nil, &admin, true); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method: %d", response.Code)
	}
}
