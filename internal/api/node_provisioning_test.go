package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/nodes"
	"gamenode/internal/provisioning"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

// nodeProvisionFixture wires a full Server (real provisioning.Service, real
// nodes.Service) so the machine-authenticated POST /api/v1/node/provisioning
// path can be exercised end to end, exactly the way a controller's
// internal/remote.Client would reach it - never a real Docker engine or
// network call.
type nodeProvisionFixture struct {
	handler        http.Handler
	db             *sql.DB
	template       templates.Template
	nativeTemplate templates.Template
	credential     string
}

func newNodeProvisionFixture(t *testing.T, installer *execFakeContainerInstaller) nodeProvisionFixture {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	templateService := templates.NewService(templates.NewStore(db))
	store := templates.NewStore(db)
	containerTemplate, err := store.Create(context.Background(), templates.Template{
		Name: "Node Container Fixture", Description: "node provisioning", SourceType: templates.SourcePelicanPterodactyl,
		Installer: templates.InstallerDefinition{Type: templates.InstallerSteamCMD, SteamCMD: &templates.SteamCMDPlan{AppID: 10, Validate: true, LoginMode: "anonymous", Platform: "native", InstallTarget: "server_root"}},
		Launch:    &templates.LaunchDefinition{Executable: "game.exe", WorkingRoot: "server_root"},
		ContainerRuntime: &templates.ContainerEggRuntimePlan{
			Images: []string{"ghcr.io/example/game:1"}, InstallerImage: "ghcr.io/example/installer:1",
			InstallerEntrypoint: "/bin/sh", InstallationScript: "echo installing", StartupTemplate: "./server",
			StartupShell: "/bin/sh", ResourceDefaults: templates.ContainerResourceDefaults{MemoryLimitBytes: 64 << 20, CPULimitMillis: 100, PIDsLimit: 32, TempSizeBytes: 1 << 20},
		},
		Compatibility:          templates.Compatibility{Status: templates.PartiallyCompatible},
		ContainerCompatibility: templates.Compatibility{Status: templates.Compatible},
	})
	if err != nil {
		t.Fatal(err)
	}
	nativeTemplate, err := store.Create(context.Background(), templates.Template{
		Name: "Node Native Fixture", Description: "node provisioning native", SourceType: templates.SourcePelicanPterodactyl,
		Installer:     templates.InstallerDefinition{Type: templates.InstallerSteamCMD, SteamCMD: &templates.SteamCMDPlan{AppID: 11, Validate: true, LoginMode: "anonymous", Platform: "windows", InstallTarget: "server_root"}},
		Launch:        &templates.LaunchDefinition{Executable: "game.exe", WorkingRoot: "server_root"},
		Compatibility: templates.Compatibility{Status: templates.Compatible},
	})
	if err != nil {
		t.Fatal(err)
	}

	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	provisioner := provisioning.NewWithOptions(db, templateService, &fakeSteamInstaller{}, serverService, t.TempDir(), provisioning.Options{
		HostOS: "windows", ContainerInstaller: installer, ImagePolicy: provisioning.ImagePolicy{AllowedRegistries: []string{"ghcr.io"}},
	})
	t.Cleanup(provisioner.Close)

	nodesService := nodes.New(db)
	credential, err := nodesService.IssueTrustedCaller(context.Background(), "controller")
	if err != nil {
		t.Fatal(err)
	}
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{
		Templates: templateService, Provisioning: provisioner, RemoteNodes: nodesService,
	}).Handler(http.NotFoundHandler())

	return nodeProvisionFixture{handler: handler, db: db, template: containerTemplate, nativeTemplate: nativeTemplate, credential: credential}
}

func nodeProvisionRequest(h http.Handler, method, path string, body []byte, credential string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestNodeProvisioningRequiresMachineAuth(t *testing.T) {
	fixture := newNodeProvisionFixture(t, &execFakeContainerInstaller{})
	response := nodeProvisionRequest(fixture.handler, http.MethodPost, "/api/v1/node/provisioning", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/example/game:1"), "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a machine credential, got %d %s", response.Code, response.Body.String())
	}
	response = nodeProvisionRequest(fixture.handler, http.MethodPost, "/api/v1/node/provisioning", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/example/game:1"), "wrong-credential")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a wrong credential, got %d %s", response.Code, response.Body.String())
	}
}

// TestNodeProvisioningForwardsToExistingProvisioningService confirms the
// machine-authenticated path forwards straight into the node's own
// provisioning.Service - the exact same Job type and phases a local,
// browser-originated request produces.
func TestNodeProvisioningForwardsToExistingProvisioningService(t *testing.T) {
	fixture := newNodeProvisionFixture(t, &execFakeContainerInstaller{})
	response := nodeProvisionRequest(fixture.handler, http.MethodPost, "/api/v1/node/provisioning", executeBody(fixture.template.ID, "Node Container", "node-container", "container", "ghcr.io/example/game:1"), fixture.credential)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	var job provisioning.Job
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	if job.RuntimeType != "container" || job.ID == "" {
		t.Fatalf("expected a container job, got %+v", job)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		statusResponse := nodeProvisionRequest(fixture.handler, http.MethodGet, "/api/v1/node/provisioning/"+job.ID, nil, fixture.credential)
		if statusResponse.Code != http.StatusOK {
			t.Fatalf("status: %d %s", statusResponse.Code, statusResponse.Body.String())
		}
		json.NewDecoder(statusResponse.Body).Decode(&job)
		if job.Status == provisioning.Completed || job.Status == provisioning.Failed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status != provisioning.Completed {
		t.Fatalf("expected the job to complete, got %+v", job)
	}
}

// TestNodeProvisioningJobStatusRequiresMachineAuth confirms the job status
// and cancel sub-routes are gated the same way the create route is.
func TestNodeProvisioningJobStatusRequiresMachineAuth(t *testing.T) {
	fixture := newNodeProvisionFixture(t, &execFakeContainerInstaller{})
	if response := nodeProvisionRequest(fixture.handler, http.MethodGet, "/api/v1/node/provisioning/does-not-exist", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
	if response := nodeProvisionRequest(fixture.handler, http.MethodPost, "/api/v1/node/provisioning/does-not-exist/cancel", nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

// TestNodeProvisioningInvalidImageRejected confirms the node's own image
// allowlist/Egg-declared-image validation still applies to a
// machine-originated request - no bypass through this path.
func TestNodeProvisioningInvalidImageRejected(t *testing.T) {
	fixture := newNodeProvisionFixture(t, &execFakeContainerInstaller{})
	response := nodeProvisionRequest(fixture.handler, http.MethodPost, "/api/v1/node/provisioning", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/not-declared/image:1"), fixture.credential)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for an undeclared image, got %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	json.NewDecoder(response.Body).Decode(&body)
	if body.Error.Code != "container_image_not_declared" {
		t.Fatalf("expected container_image_not_declared, got %+v", body.Error)
	}
}

// TestNodeProvisioningNonProvisionableTemplateRejected mirrors the local
// route's not_provisionable rejection for a template with no container
// runtime plan.
func TestNodeProvisioningNonProvisionableTemplateRejected(t *testing.T) {
	fixture := newNodeProvisionFixture(t, &execFakeContainerInstaller{})
	response := nodeProvisionRequest(fixture.handler, http.MethodPost, "/api/v1/node/provisioning", executeBody(fixture.nativeTemplate.ID, "s", "s", "container", ""), fixture.credential)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d %s", response.Code, response.Body.String())
	}
}

// TestNodeProvisioningCancel confirms POST .../cancel forwards to the same
// provisioning.Service.Cancel a local operator's cancel already uses.
func TestNodeProvisioningCancel(t *testing.T) {
	fixture := newNodeProvisionFixture(t, &execFakeContainerInstaller{})
	start := nodeProvisionRequest(fixture.handler, http.MethodPost, "/api/v1/node/provisioning", executeBody(fixture.template.ID, "Cancel Me", "cancel-me", "container", "ghcr.io/example/game:1"), fixture.credential)
	if start.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	var job provisioning.Job
	json.NewDecoder(start.Body).Decode(&job)

	cancel := nodeProvisionRequest(fixture.handler, http.MethodPost, "/api/v1/node/provisioning/"+job.ID+"/cancel", nil, fixture.credential)
	// The job may already have completed by the time cancel runs (the fake
	// installer is effectively instant); either a successful cancel or a
	// job_not_active conflict is an acceptable, controlled outcome - never a
	// 500 or an unrelated job being cancelled.
	if cancel.Code != http.StatusOK && cancel.Code != http.StatusConflict {
		t.Fatalf("cancel: %d %s", cancel.Code, cancel.Body.String())
	}
}
