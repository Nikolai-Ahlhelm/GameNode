package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamenode/internal/gameconfig"
	gameRuntime "gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/templates"
)

const provisioningSecret = "PROVISIONING_SECRET_SHOULD_NEVER_APPEAR"

func valheimTemplate(t *testing.T) templates.Template {
	t.Helper()
	templateData, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "valheim", "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var template templates.Template
	if err = json.Unmarshal(templateData, &template); err != nil {
		t.Fatal(err)
	}
	adapterData, err := os.ReadFile(filepath.Join("..", "..", "templates", "steamcmd", "valheim", "valheim-settings.adapter.json"))
	if err != nil {
		t.Fatal(err)
	}
	var adapter templates.ConfigAdapterDefinition
	if err = json.Unmarshal(adapterData, &adapter); err != nil {
		t.Fatal(err)
	}
	template.ResolvedAdapters = []templates.ConfigAdapterDefinition{adapter}
	return template
}

// TestValheimProvisioningPersistsManagedLaunchConfiguration runs the real
// provisioning flow with a fake installer and checks that managed settings are
// persisted as typed values, kept out of the process environment, and kept out
// of job state.
func TestValheimProvisioningPersistsManagedLaunchConfiguration(t *testing.T) {
	db, data, _ := provisionFixture(t)
	defer db.Close()
	template := valheimTemplate(t)
	installer := &fakeInstaller{createExecutable: true, executable: "valheim_server.exe"}
	serverService := servers.NewService(servers.NewStore(db), gameRuntime.NewNative())
	service := NewWithOptions(db, &templateSource{template: template}, installer, serverService, data, Options{HostOS: "windows"})
	defer service.Close()
	values := map[string]string{"SERVER_NAME": "My Valheim", "WORLD_NAME": "Dedicated", "SERVER_PASSWORD": provisioningSecret, "PUBLIC_VISIBILITY": "false", "CROSSPLAY": "true", "SAVE_INTERVAL_SECONDS": "1800", "SERVER_PORT": "2456"}
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Valheim", DirectoryName: "valheim", Values: values, ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.Status != Completed || job.ServerID == "" {
		t.Fatalf("job=%#v", job)
	}

	// The adapter snapshot and its initial values are persisted together.
	var adapterCount, valueCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_adapters WHERE server_id=? AND adapter_id='valheim-settings' AND adapter_schema_version=2`, job.ServerID).Scan(&adapterCount); err != nil || adapterCount != 1 {
		t.Fatalf("adapter snapshot count=%d err=%v", adapterCount, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_values WHERE server_id=?`, job.ServerID).Scan(&valueCount); err != nil || valueCount != 6 {
		t.Fatalf("managed value count=%d err=%v", valueCount, err)
	}
	var storedSecret string
	if err = db.QueryRow(`SELECT value FROM server_config_values WHERE server_id=? AND field_key='SERVER_PASSWORD' AND sensitive=1`, job.ServerID).Scan(&storedSecret); err != nil || storedSecret != provisioningSecret {
		t.Fatalf("secret was not persisted as a sensitive managed value: %v", err)
	}

	// Managed keys must not become process environment entries or template
	// variable metadata, so there is exactly one source of truth.
	record, err := serverService.Get(context.Background(), job.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	for key := range record.Server.EnvironmentVariables {
		if key == "SERVER_NAME" || key == "SERVER_PASSWORD" || key == "CROSSPLAY" {
			t.Fatalf("managed key leaked into the process environment: %s", key)
		}
	}
	if record.Server.EnvironmentVariables["SERVER_PORT"] != "2456" {
		t.Fatalf("unmanaged template variables must still reach the environment: %#v", record.Server.EnvironmentVariables)
	}
	var variableCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_template_variables WHERE server_id=? AND variable_key='SERVER_PASSWORD'`, job.ServerID).Scan(&variableCount); err != nil || variableCount != 0 {
		t.Fatalf("managed secret must not appear in template variable metadata: %d %v", variableCount, err)
	}

	// The base launch stays free of managed settings.
	joined := strings.Join(record.Server.Arguments, " ")
	if strings.Contains(joined, provisioningSecret) || strings.Contains(joined, "-name") || strings.Contains(joined, "-crossplay") {
		t.Fatalf("base launch must not contain managed settings: %#v", record.Server.Arguments)
	}
	if !strings.Contains(joined, "-port") || !strings.Contains(joined, "2456") {
		t.Fatalf("base launch lost its static arguments: %#v", record.Server.Arguments)
	}

	// Resolution produces the full runtime launch.
	configService := gameconfig.New(db, serverService)
	arguments, environment, err := configService.ResolveLaunch(context.Background(), job.ServerID, record.Server.Arguments, record.Server.EnvironmentVariables)
	if err != nil {
		t.Fatal(err)
	}
	resolved := strings.Join(arguments, " ")
	for _, wanted := range []string{"-name My Valheim", "-world Dedicated", "-password " + provisioningSecret, "-public 0", "-crossplay", "-saveinterval 1800"} {
		if !strings.Contains(resolved, wanted) {
			t.Fatalf("resolved launch is missing %q: %#v", wanted, arguments)
		}
	}
	if environment["STEAMAPPID"] != "892970" {
		t.Fatalf("base environment was lost: %#v", environment)
	}

	// Job state, events, and the persisted registration snapshot must not carry
	// the secret.
	var jobText string
	if err = db.QueryRow(`SELECT COALESCE(GROUP_CONCAT(COALESCE(registration_snapshot_json,'')||COALESCE(error_summary,'')||COALESCE(status,'')),'') FROM provisioning_jobs`).Scan(&jobText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jobText, provisioningSecret) {
		t.Fatal("provisioning job state leaked the managed secret")
	}
	var eventText string
	if err = db.QueryRow(`SELECT COALESCE(GROUP_CONCAT(COALESCE(summary,'')||COALESCE(phase,'')||COALESCE(code,'')),'') FROM provisioning_job_events`).Scan(&eventText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(eventText, provisioningSecret) {
		t.Fatal("provisioning events leaked the managed secret")
	}
}

// TestRedactedAdaptersDropsOnlySecretsAndReportsThem keeps non-secret values
// replayable while signalling that secret material was removed.
func TestRedactedAdaptersDropsOnlySecretsAndReportsThem(t *testing.T) {
	adapters := []servers.ProvisionedConfigAdapter{{ID: "settings", Values: []servers.ProvisionedConfigValue{{Key: "SERVER_NAME", Value: "Public"}, {Key: "SERVER_PASSWORD", Value: provisioningSecret, Sensitive: true}}}}
	redacted, hadSecrets := redactedAdapters(adapters)
	if !hadSecrets {
		t.Fatal("removing a secret value must be reported to the caller")
	}
	if len(redacted) != 1 || len(redacted[0].Values) != 1 || redacted[0].Values[0].Key != "SERVER_NAME" {
		t.Fatalf("unexpected redaction: %#v", redacted)
	}
	if len(adapters[0].Values) != 2 {
		t.Fatal("redaction must not mutate the original adapter values")
	}
	plain := []servers.ProvisionedConfigAdapter{{ID: "settings", Values: []servers.ProvisionedConfigValue{{Key: "SERVER_NAME", Value: "Public"}}}}
	if _, reported := redactedAdapters(plain); reported {
		t.Fatal("an adapter without secrets must stay replayable")
	}
}

// TestFailedRegistrationWithManagedSecretIsNotRecoverable covers the exact
// recoverable-registration case: a registration that carries managed secrets
// must not be retryable, because the secret is deliberately never written to
// job state and a replay would create a server with silently missing values.
func TestFailedRegistrationWithManagedSecretIsNotRecoverable(t *testing.T) {
	db, data, _ := provisionFixture(t)
	defer db.Close()
	template := valheimTemplate(t)
	installer := &fakeInstaller{createExecutable: true, executable: "valheim_server.exe"}
	service := NewWithOptions(db, &templateSource{template: template}, installer, &failingCreator{}, data, Options{HostOS: "windows"})
	defer service.Close()
	values := map[string]string{"SERVER_NAME": "My Valheim", "WORLD_NAME": "Dedicated", "SERVER_PASSWORD": provisioningSecret, "PUBLIC_VISIBILITY": "false", "CROSSPLAY": "true", "SAVE_INTERVAL_SECONDS": "1800", "SERVER_PORT": "2456"}
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Valheim", DirectoryName: "valheim-retry", Values: values, ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.Status != Failed || job.FailurePhase != RegisteringServer || !job.InstallationCompleted {
		t.Fatalf("unexpected registration failure state: %#v", job)
	}
	if job.RegistrationRecoverable {
		t.Fatal("a registration carrying managed secrets must not be recoverable")
	}
	// No snapshot is persisted at all, so no redacted copy can be replayed.
	var snapshot string
	if err = db.QueryRow(`SELECT registration_snapshot_json FROM provisioning_jobs WHERE id=?`, job.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != "" {
		t.Fatalf("no registration snapshot may be stored for a managed-secret registration: %q", snapshot)
	}
	if strings.Contains(job.ErrorSummary, provisioningSecret) || strings.Contains(job.Summary, provisioningSecret) {
		t.Fatal("job state leaked the managed secret")
	}
	if !strings.Contains(job.ErrorSummary, "cannot be retried") {
		t.Fatalf("the operator must be told the registration cannot be retried: %q", job.ErrorSummary)
	}
	// The retry entry point must refuse rather than create a server without its
	// managed secrets.
	if _, err = service.RetryRegistration(context.Background(), job.ID, "actor"); !errors.Is(err, ErrRecoveryUnavailable) {
		t.Fatalf("retry must be unavailable, got %v", err)
	}
	var serverCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM servers`).Scan(&serverCount); err != nil || serverCount != 0 {
		t.Fatalf("no server may exist after a refused retry: %d %v", serverCount, err)
	}
	var managedValueCount int
	if err = db.QueryRow(`SELECT COUNT(*) FROM server_config_values`).Scan(&managedValueCount); err != nil || managedValueCount != 0 {
		t.Fatalf("no managed values may exist after a refused retry: %d %v", managedValueCount, err)
	}
}

// TestFailedRegistrationWithoutManagedSecretsStaysRecoverable proves the
// restriction is narrow: a template whose managed settings carry no secret
// keeps the existing retry behaviour.
func TestFailedRegistrationWithoutManagedSecretsStaysRecoverable(t *testing.T) {
	db, data, _ := provisionFixture(t)
	defer db.Close()
	template := valheimTemplate(t)
	installer := &fakeInstaller{createExecutable: true, executable: "valheim_server.exe"}
	service := NewWithOptions(db, &templateSource{template: template}, installer, &failingCreator{}, data, Options{HostOS: "windows"})
	defer service.Close()
	// SERVER_PASSWORD is nullable, so an empty value stores no secret at all.
	values := map[string]string{"SERVER_NAME": "My Valheim", "WORLD_NAME": "Dedicated", "SERVER_PASSWORD": "", "PUBLIC_VISIBILITY": "false", "CROSSPLAY": "true", "SAVE_INTERVAL_SECONDS": "1800", "SERVER_PORT": "2456"}
	job, err := service.Start(context.Background(), Request{TemplateID: template.ID, ServerName: "Valheim Open", DirectoryName: "valheim-open", Values: values, ActorUserID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	job = waitTerminal(t, service, job.ID)
	if job.Status != Failed || !job.InstallationCompleted || !job.RegistrationRecoverable {
		t.Fatalf("a secret-free managed registration must stay recoverable: %#v", job)
	}
	var snapshot string
	if err = db.QueryRow(`SELECT registration_snapshot_json FROM provisioning_jobs WHERE id=?`, job.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, "SERVER_NAME") {
		t.Fatalf("non-secret managed values must be replayable: %q", snapshot)
	}
}
