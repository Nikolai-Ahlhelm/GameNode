package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/runtime"
	"gamenode/internal/scheduler"
	"gamenode/internal/servers"
)

func TestRestartScheduleAPIRequiresCSRFAndSupportsCRUD(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "server.exe"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	serverService := servers.NewService(servers.NewStore(db), runtime.NewNative())
	record, err := serverService.Create(context.Background(), servers.Server{TenantID: "default", Name: "Scheduled API", WorkingDirectory: root, Executable: "server.exe"})
	if err != nil {
		t.Fatal(err)
	}
	store := scheduler.NewStore(db)
	h := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{RestartSchedules: store}).Handler(http.NotFoundHandler())

	setup := httptest.NewRecorder()
	h.ServeHTTP(setup, httptest.NewRequest(http.MethodPost, "/api/v1/setup", bytes.NewBufferString(`{"username":"admin","email":"admin@example.test","password":"12345678"}`)))
	if setup.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setup.Code, setup.Body.String())
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err = json.NewDecoder(setup.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	cookie := setup.Result().Cookies()[0]
	request := func(method, path, body string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.AddCookie(cookie)
		if csrf {
			r.Header.Set("X-CSRF-Token", session.CSRF)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if response := request(http.MethodGet, "/api/v1/servers/"+record.Server.ID+"/restart-schedules", "", false); response.Code != http.StatusOK {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/api/v1/servers/"+record.Server.ID+"/restart-schedules", `{"schedule_type":"daily","time_of_day":"04:00","time_zone":"Not/AZone"}`, true); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid timezone: %d %s", response.Code, response.Body.String())
	}
	createdResponse := request(http.MethodPost, "/api/v1/servers/"+record.Server.ID+"/restart-schedules", `{"schedule_type":"weekly","time_of_day":"04:00","day_of_week":0,"time_zone":"Europe/Berlin"}`, true)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err = json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || !created.Enabled {
		t.Fatalf("unexpected created schedule: %#v", created)
	}
	if response := request(http.MethodPatch, "/api/v1/servers/"+record.Server.ID+"/restart-schedules/"+created.ID, `{"enabled":false}`, false); response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPatch, "/api/v1/servers/"+record.Server.ID+"/restart-schedules/"+created.ID, `{"enabled":false}`, true); response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte("next_restart_at")) {
		t.Fatalf("disable: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodDelete, "/api/v1/servers/"+record.Server.ID+"/restart-schedules/"+created.ID, "", true); response.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	events, err := audit.New(db).List(context.Background(), audit.Filter{ServerID: &record.Server.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 {
		t.Fatalf("schedule mutations were not audited: %d", len(events))
	}
}
