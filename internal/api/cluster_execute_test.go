package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/database"
	"gamenode/internal/identity"
	"gamenode/internal/nodes"
	"gamenode/internal/provisioning"
	"gamenode/internal/rbac"
	"gamenode/internal/remote"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
	"gamenode/internal/tenants"
)

// execFakeContainerInstaller is a minimal, controllable stand-in for
// provisioning.ContainerInstaller so these tests never touch a real Docker
// engine - the same discipline internal/provisioning's own tests use (see
// internal/provisioning/provisioning_test.go's fakeContainerInstaller).
type execFakeContainerInstaller struct{ fail bool }

func (i *execFakeContainerInstaller) Available(context.Context) error         { return nil }
func (i *execFakeContainerInstaller) PullImage(context.Context, string) error { return nil }
func (i *execFakeContainerInstaller) RunInstaller(_ context.Context, _ gameRuntime.ContainerInstallSpec, output io.Writer) error {
	if i.fail {
		return fmt.Errorf("installer failed")
	}
	if output != nil {
		_, _ = io.WriteString(output, "installing\n")
	}
	return nil
}

type execFixture struct {
	handler        http.Handler
	db             *sql.DB
	template       templates.Template
	nativeTemplate templates.Template
	provisioning   *provisioning.Service
	servers        *servers.Service
	nodesService   *nodes.Service
	remote         *fakeRemoteClient
}

// newClusterExecuteAPI builds a full Server wired with a real
// provisioning.Service (container-capable via execFakeContainerInstaller),
// a real nodes.Service (so Remote Nodes can be enrolled), and a
// fakeRemoteClient standing in for internal/remote.Client - exactly what
// POST /api/v1/cluster/placement/execute needs end to end, without a real
// Docker engine or network call anywhere in the test.
func newClusterExecuteAPI(t *testing.T, installer *execFakeContainerInstaller) execFixture {
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
		Name: "Container Fixture", Description: "container placement", SourceType: templates.SourcePelicanPterodactyl,
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
		Name: "Native Fixture", Description: "native placement", SourceType: templates.SourcePelicanPterodactyl,
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

	remoteClient := &fakeRemoteClient{}
	nodesService := nodes.New(db)
	handler := api.New(auth.New(db), serverService, slog.New(slog.NewTextHandler(io.Discard, nil)), false, api.Options{
		Templates: templateService, Provisioning: provisioner, RemoteNodes: nodesService, RemoteClient: remoteClient,
	}).Handler(http.NotFoundHandler())

	return execFixture{handler: handler, db: db, template: containerTemplate, nativeTemplate: nativeTemplate, provisioning: provisioner, servers: serverService, nodesService: nodesService, remote: remoteClient}
}

// fakeSteamInstaller satisfies provisioning.Installer for the native
// (non-container) leg of a request; it always "succeeds" by writing the
// declared launch executable, mirroring internal/provisioning's own test
// fakes.
type fakeSteamInstaller struct{ fail bool }

func (f *fakeSteamInstaller) Install(ctx context.Context, root string, _ steamcmd.InstallPlan, output io.Writer, sink steamcmd.EventSink) error {
	if f.fail {
		return fmt.Errorf("install failed")
	}
	return os.WriteFile(filepath.Join(root, "game.exe"), []byte("test"), 0600)
}

func executeBody(templateID, name, directory, runtimeType, image string) []byte {
	body, _ := json.Marshal(map[string]any{
		"template_id": templateID, "server_name": name, "directory_name": directory, "runtime_type": runtimeType, "image": image,
	})
	return body
}

// fillLocalNodeCapacity directly registers count already-"installed" native
// servers (bypassing HTTP/provisioning) so placement.LocalCandidate's
// Available() reaches zero and the local node is excluded as a placement
// candidate - the only way, short of a real second machine, to force
// placement.Decide to select an enrolled Remote Node in a test.
func fillLocalNodeCapacity(t *testing.T, serverService *servers.Service, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		dir := t.TempDir()
		exe := filepath.Join(dir, "run.bin")
		if err := os.WriteFile(exe, []byte("x"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := serverService.Create(context.Background(), servers.Server{Name: fmt.Sprintf("filler-%d", i), WorkingDirectory: dir, Executable: exe}); err != nil {
			t.Fatal(err)
		}
	}
}

func enrollFakeRemoteNode(t *testing.T, svc *nodes.Service, capabilities []string) nodes.RemoteNode {
	t.Helper()
	n, err := svc.CreateEnrolled(context.Background(), nodes.CreateEnrolledInput{
		DisplayName: "Remote Fixture", Endpoint: "https://remote.example.test", Credential: "test-credential",
		NodeID: "remote-fixture", ProtocolVersion: 1, GameNodeVersion: "test", OS: "linux", Arch: "amd64", Capabilities: capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func createTestTenant(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	tenant, err := tenants.New(db).Create(context.Background(), tenants.CreateInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return tenant.ID
}

func createUserWithPermissions(t *testing.T, db *sql.DB, username string, permissions []string, scope rbac.Scope) string {
	t.Helper()
	user, err := identity.New(db).CreateUser(context.Background(), identity.CreateUserInput{Username: username, Email: username + "@example.test", Password: "a password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	grantPermissions(t, db, user.ID, username+"-role-1", permissions, scope)
	return user.ID
}

// grantPermissions assigns an additional role to an already-existing user,
// for tests that need more than one scope (e.g. a global-only permission
// plus a tenant-scoped one) on the same account.
func grantPermissions(t *testing.T, db *sql.DB, userID, roleName string, permissions []string, scope rbac.Scope) {
	t.Helper()
	ctx := context.Background()
	authorization := rbac.New(db)
	role, err := authorization.CreateRole(ctx, roleName, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := authorization.ReplacePermissions(ctx, role.ID, permissions); err != nil {
		t.Fatal(err)
	}
	if err := authorization.AssignUser(ctx, userID, role.ID, scope); err != nil {
		t.Fatal(err)
	}
}

func TestClusterPlacementExecuteRequiresAuth(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/example/game:1"), nil, false)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated=401, got %d %s", response.Code, response.Body.String())
	}
}

func TestClusterPlacementExecuteCSRFRequired(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/example/game:1"), &admin, false)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected CSRF rejection, got %d %s", response.Code, response.Body.String())
	}
}

// TestClusterPlacementExecuteLocalContainerUsesExistingProvisioningPath is
// the central proof for this milestone: a container placement execution
// request against the local node ends up as an ordinary
// provisioning.Service job (same Job type, same phases) and produces a
// servers.Server with RuntimeType=container - never a native fallback and
// never a second container-lifecycle implementation.
func TestClusterPlacementExecuteLocalContainerUsesExistingProvisioningPath(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "Container Server", "container-server", "container", "ghcr.io/example/game:1"), &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("execute: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Decision struct {
			Execution string `json:"execution"`
			Selected  struct {
				Kind string `json:"kind"`
			} `json:"selected"`
		} `json:"decision"`
		Job struct {
			ID          string `json:"id"`
			RuntimeType string `json:"runtime_type"`
		} `json:"job"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Decision.Selected.Kind != "local" || body.Decision.Execution != "local_only" {
		t.Fatalf("expected local_only execution, got %+v", body.Decision)
	}
	if body.Job.RuntimeType != "container" || body.Job.ID == "" {
		t.Fatalf("expected a container provisioning job, got %+v", body.Job)
	}

	// Poll the ordinary local provisioning job status route - the same
	// route a browser-originated request already uses - confirming no
	// second job-tracking mechanism exists for placement-originated jobs.
	deadline := time.Now().Add(3 * time.Second)
	var job map[string]any
	for time.Now().Before(deadline) {
		statusResponse := templateRequest(fixture.handler, http.MethodGet, "/api/v1/provisioning/jobs/"+body.Job.ID, nil, &admin, false)
		if statusResponse.Code != http.StatusOK {
			t.Fatalf("job status: %d %s", statusResponse.Code, statusResponse.Body.String())
		}
		json.NewDecoder(statusResponse.Body).Decode(&job)
		if job["status"] == "completed" || job["status"] == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job["status"] != "completed" {
		t.Fatalf("expected the placement-originated job to complete via the ordinary provisioning path, got %+v", job)
	}
	serverID, _ := job["server_id"].(string)
	if serverID == "" {
		t.Fatal("expected the completed job to register a server")
	}
	record, err := fixture.servers.Get(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Server.RuntimeType != servers.RuntimeContainer || record.Server.Container == nil {
		t.Fatalf("expected a container server, got runtime_type=%q container=%v", record.Server.RuntimeType, record.Server.Container)
	}

	events, err := audit.New(fixture.db).List(context.Background(), audit.Filter{Action: audit.ClusterPlacementExecute})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Result != audit.Success {
		t.Fatalf("expected exactly one successful cluster placement execute audit event, got %#v", events)
	}
}

// TestClusterPlacementExecuteLocalNativeUsesExistingCreatePath confirms
// runtime_type=native still goes through the same already-existing
// provisioning path, unchanged by this milestone.
func TestClusterPlacementExecuteLocalNativeUsesExistingCreatePath(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.nativeTemplate.ID, "Native Server", "native-server", "native", ""), &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("execute: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Job struct {
			RuntimeType string `json:"runtime_type"`
		} `json:"job"`
	}
	json.NewDecoder(response.Body).Decode(&body)
	if body.Job.RuntimeType != "native" {
		t.Fatalf("expected a native provisioning job, got %+v", body.Job)
	}
}

// TestClusterPlacementExecuteRejectedDecisionNeverExecutes confirms that
// when no candidate is eligible (missing_capability), no provisioning job is
// created anywhere and the response reflects the rejection, not a fallback.
func TestClusterPlacementExecuteRejectedDecisionNeverExecutes(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	fillLocalNodeCapacity(t, fixture.servers, 50) // local excluded; no remote enrolled -> no_eligible_node

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/example/game:1"), &admin, true)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a rejected decision, got %d %s", response.Code, response.Body.String())
	}
	if fixture.remote.startCalls != 0 {
		t.Fatalf("expected no remote dispatch for a rejected decision, got %d calls", fixture.remote.startCalls)
	}
	events, err := audit.New(fixture.db).List(context.Background(), audit.Filter{Action: audit.ClusterPlacementExecute})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Result != audit.Failure {
		t.Fatalf("expected exactly one failed cluster placement execute audit event, got %#v", events)
	}
}

// TestClusterPlacementExecuteRemoteContainerUsesNodeAPI forces the local
// node out of contention (capacity exhausted) so the sole enrolled Remote
// Node is selected, then confirms execution dispatches through
// dispatchRemoteProvisioning/internal/remote.Client.StartProvisioning - the
// machine-authenticated Node API - never a native fallback and never a
// direct database/Docker call against the remote node.
func TestClusterPlacementExecuteRemoteContainerUsesNodeAPI(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	fillLocalNodeCapacity(t, fixture.servers, 50)
	remoteNode := enrollFakeRemoteNode(t, fixture.nodesService, []string{"container_runtime", "native_runtime"})
	fixture.remote.startJob = provisioning.Job{ID: "remote-job-1", RuntimeType: "container", Status: provisioning.Pending, TenantID: "default"}

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "Remote Container", "remote-container", "container", "ghcr.io/example/game:1"), &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("execute: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Decision struct {
			Execution string `json:"execution"`
			Selected  struct {
				NodeID string `json:"node_id"`
				Kind   string `json:"kind"`
			} `json:"selected"`
		} `json:"decision"`
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Decision.Selected.Kind != "remote" || body.Decision.Selected.NodeID != remoteNode.ID {
		t.Fatalf("expected the remote node selected, got %+v", body.Decision)
	}
	if body.Decision.Execution != "remote_provisioning" {
		t.Fatalf("expected execution=remote_provisioning, got %q", body.Decision.Execution)
	}
	if fixture.remote.startCalls != 1 {
		t.Fatalf("expected exactly one remote StartProvisioning call, got %d", fixture.remote.startCalls)
	}
	if fixture.remote.lastStart.TemplateID != fixture.template.ID || fixture.remote.lastStart.RuntimeType != "container" || fixture.remote.lastStart.Image != "ghcr.io/example/game:1" {
		t.Fatalf("unexpected forwarded request: %+v", fixture.remote.lastStart)
	}
	if body.Job.ID != "remote-job-1" {
		t.Fatalf("expected the remote job to be returned, got %+v", body.Job)
	}
}

// TestClusterPlacementExecuteRemoteNodeOffline confirms an unreachable
// Remote Node surfaces as a bad-gateway style error, never a silent native
// fallback and never a job created anywhere.
func TestClusterPlacementExecuteRemoteNodeOffline(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	fillLocalNodeCapacity(t, fixture.servers, 50)
	enrollFakeRemoteNode(t, fixture.nodesService, []string{"container_runtime"})
	fixture.remote.startErr = &remote.Error{Kind: remote.KindUnreachable, Detail: "connection failed"}

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/example/game:1"), &admin, true)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for an unreachable remote node, got %d %s", response.Code, response.Body.String())
	}
	events, err := audit.New(fixture.db).List(context.Background(), audit.Filter{Action: audit.ClusterPlacementExecute})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Result != audit.Failure {
		t.Fatalf("expected exactly one failed cluster placement execute audit event, got %#v", events)
	}
}

// TestClusterPlacementExecuteMissingCapabilityRejected confirms a Remote
// Node that does not advertise container_runtime is excluded as a candidate
// (missing_capability), so with local capacity exhausted the request is
// rejected outright rather than silently executing against an incapable
// node.
func TestClusterPlacementExecuteMissingCapabilityRejected(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	fillLocalNodeCapacity(t, fixture.servers, 50)
	enrollFakeRemoteNode(t, fixture.nodesService, []string{"native_runtime"}) // no container_runtime

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/example/game:1"), &admin, true)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a missing-capability rejection, got %d %s", response.Code, response.Body.String())
	}
	if fixture.remote.startCalls != 0 {
		t.Fatalf("expected no remote dispatch when the only candidate lacks the capability, got %d calls", fixture.remote.startCalls)
	}
}

// TestClusterPlacementExecuteRBACRequiresClusterSchedule confirms a caller
// with Server.Create/Templates.View but no Cluster.Schedule is forbidden -
// placement execution is not reachable through the ordinary provisioning
// permission alone.
func TestClusterPlacementExecuteRBACRequiresClusterSchedule(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	createAdminSession(t, fixture.handler) // completes initial setup so a second user can log in
	createUserWithPermissions(t, fixture.db, "no-schedule", []string{"Templates.View", "Server.Create"}, rbac.Scope{Type: "global"})
	session := loginSession(t, fixture.handler, "no-schedule")

	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/example/game:1"), &session, true)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without Cluster.Schedule, got %d %s", response.Code, response.Body.String())
	}
}

// TestClusterPlacementExecuteTenantIsolation confirms a Cluster.Schedule +
// Server.Create grant scoped to tenant A never authorizes execution for
// tenant B, mirroring TestClusterPlacementTenantIsolation for the decision
// endpoint.
func TestClusterPlacementExecuteTenantIsolation(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	createAdminSession(t, fixture.handler) // completes initial setup so a second user can log in
	tenantA := createTestTenant(t, fixture.db, "Exec Tenant A")
	tenantB := createTestTenant(t, fixture.db, "Exec Tenant B")
	// Templates.View is global-only (see rbac.GlobalOnly) and must be
	// granted through a separate global-scoped role; Cluster.Schedule and
	// Server.Create are tenant-assignable and scoped to tenant A only.
	userID := createUserWithPermissions(t, fixture.db, "exec-scoped", []string{"Templates.View"}, rbac.Scope{Type: "global"})
	grantPermissions(t, fixture.db, userID, "exec-scoped-role-2", []string{"Cluster.Schedule", "Server.Create"}, rbac.Scope{Type: "tenant", ID: &tenantA})
	session := loginSession(t, fixture.handler, "exec-scoped")

	okBody, _ := json.Marshal(map[string]any{"tenant_id": tenantA, "template_id": fixture.template.ID, "server_name": "a", "directory_name": "a", "runtime_type": "container", "image": "ghcr.io/example/game:1"})
	if response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", okBody, &session, true); response.Code != http.StatusAccepted {
		t.Fatalf("expected tenant A execution to succeed, got %d %s", response.Code, response.Body.String())
	}

	crossBody, _ := json.Marshal(map[string]any{"tenant_id": tenantB, "template_id": fixture.template.ID, "server_name": "b", "directory_name": "b", "runtime_type": "container", "image": "ghcr.io/example/game:1"})
	if response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", crossBody, &session, true); response.Code != http.StatusForbidden {
		t.Fatalf("expected cross-tenant execution to be forbidden, got %d %s", response.Code, response.Body.String())
	}
}

// TestClusterPlacementExecuteInvalidImageRejected confirms an image not
// declared by the Egg is rejected synchronously by the same
// provisioning.Service validation a local request already goes through -
// never silently substituted or passed through to a container engine.
func TestClusterPlacementExecuteInvalidImageRejected(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.template.ID, "s", "s", "container", "ghcr.io/not-declared/image:1"), &admin, true)
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

// TestClusterPlacementExecuteNonProvisionableTemplateRejected confirms a
// template with no container runtime plan is rejected synchronously rather
// than silently provisioning it as a native server.
func TestClusterPlacementExecuteNonProvisionableTemplateRejected(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", executeBody(fixture.nativeTemplate.ID, "s", "s", "container", ""), &admin, true)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a non-provisionable template, got %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	json.NewDecoder(response.Body).Decode(&body)
	if body.Error.Code != "not_provisionable" {
		t.Fatalf("expected not_provisionable, got %+v", body.Error)
	}
}

// TestClusterPlacementExecuteInvalidResourceLimitsFailsJob confirms
// out-of-bounds container resource limits fail the resulting provisioning
// job (the same asynchronous validation a local container request already
// goes through) rather than being silently clamped or passed to the
// container runtime.
func TestClusterPlacementExecuteInvalidResourceLimitsFailsJob(t *testing.T) {
	fixture := newClusterExecuteAPI(t, &execFakeContainerInstaller{})
	admin := createAdminSession(t, fixture.handler)
	body, _ := json.Marshal(map[string]any{
		"template_id": fixture.template.ID, "server_name": "s", "directory_name": "s", "runtime_type": "container",
		"image": "ghcr.io/example/game:1", "memory_limit_bytes": 1, // far below the 16 MiB floor
	})
	response := templateRequest(fixture.handler, http.MethodPost, "/api/v1/cluster/placement/execute", body, &admin, true)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected the job to be accepted and fail asynchronously, got %d %s", response.Code, response.Body.String())
	}
	var accepted struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	json.NewDecoder(response.Body).Decode(&accepted)
	deadline := time.Now().Add(3 * time.Second)
	var job map[string]any
	for time.Now().Before(deadline) {
		statusResponse := templateRequest(fixture.handler, http.MethodGet, "/api/v1/provisioning/jobs/"+accepted.Job.ID, nil, &admin, false)
		json.NewDecoder(statusResponse.Body).Decode(&job)
		if job["status"] == "completed" || job["status"] == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job["status"] != "failed" {
		t.Fatalf("expected the job to fail on invalid resource limits, got %+v", job)
	}
}

// TestPlacementPackageNeverImportsRuntimeOrDocker is a static architectural
// proof for "internal/placement never performs direct Docker or runtime
// control": its own source is grepped for the runtime/Docker-facing import
// paths this product uses (internal/runtime, Docker's engine SDK, and
// os/exec), which must never appear. internal/placement only ever returns a
// node selection; provisioning.Service (invoked exclusively from
// internal/api) is the only path that ever reaches internal/runtime.
func TestPlacementPackageNeverImportsRuntimeOrDocker(t *testing.T) {
	forbidden := []string{`"gamenode/internal/runtime"`, `"os/exec"`, "docker.io/go-docker", "docker/docker/client"}
	matches, err := filepath.Glob("../placement/*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("expected to find internal/placement source files")
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		for _, needle := range forbidden {
			if strings.Contains(content, needle) {
				t.Fatalf("%s imports/uses forbidden runtime/docker surface %q - internal/placement must stay a pure decision engine", path, needle)
			}
		}
	}
}
