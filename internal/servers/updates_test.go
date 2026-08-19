package servers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBeginUpdateReservationIsExclusive(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	release, err := service.BeginUpdate(record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.BeginUpdate(record.Server.ID); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("expected ErrUpdateInProgress, got %v", err)
	}
	release()
	if release2, err := service.BeginUpdate(record.Server.ID); err != nil {
		t.Fatal(err)
	} else {
		release2()
	}
}

func TestStartRejectedWhileUpdateReserved(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	release, err := service.BeginUpdate(record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = service.Start(context.Background(), record.Server.ID); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("expected ErrUpdateInProgress, got %v", err)
	}
}

func TestRestartRejectedWhileUpdateReserved(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	// Restart's own stop phase must complete before its start phase reaches
	// the updates reservation check, so the server must actually be running.
	if _, err = service.Start(context.Background(), record.Server.ID); err != nil {
		t.Fatal(err)
	}
	release, err := service.BeginUpdate(record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = service.Restart(context.Background(), record.Server.ID); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("expected ErrUpdateInProgress, got %v", err)
	}
}

func TestDeleteRejectedWhileUpdateReserved(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	release, err := service.BeginUpdate(record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = service.Delete(context.Background(), record.Server.ID); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("expected ErrUpdateInProgress, got %v", err)
	}
}

func TestVerifyLaunchExecutablePresentAcceptsExistingExecutable(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = service.VerifyLaunchExecutablePresent(record); err != nil {
		t.Fatalf("expected the existing executable to verify, got %v", err)
	}
}

func TestVerifyLaunchExecutablePresentRejectsMissingExecutable(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	record.Server.Executable = "no-such-executable-after-update"
	if err = service.VerifyLaunchExecutablePresent(record); !errors.Is(err, ErrLaunchExecutableMissing) {
		t.Fatalf("expected ErrLaunchExecutableMissing, got %v", err)
	}
}

func TestVerifyLaunchExecutablePresentRejectsRelativeEscape(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	record, err := service.Create(context.Background(), testServer(t))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err = os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(record.Server.WorkingDirectory, outside)
	if err != nil {
		t.Fatal(err)
	}
	record.Server.Executable = relative
	if err = service.VerifyLaunchExecutablePresent(record); !errors.Is(err, ErrLaunchExecutableMissing) {
		t.Fatalf("expected ErrLaunchExecutableMissing for an escaping relative path, got %v", err)
	}
}

func TestCreateProvisionedPersistsSteamCMDMetadataOnlyWhenGiven(t *testing.T) {
	service, _, _, db := testService(t)
	defer db.Close()
	server := testServer(t)
	server.CreationMode = CreationTemplate
	steamCMD := &ProvisionedSteamCMD{InstallerType: "steamcmd", AppID: 380870, LoginMode: "anonymous", Validate: true, TemplateID: "project-zomboid", TemplateVersion: "2.0.0", TemplateSource: "official"}
	record, err := service.CreateProvisioned(context.Background(), server, "project-zomboid", nil, nil, nil, steamCMD)
	if err != nil {
		t.Fatal(err)
	}
	info, ok, err := service.SteamCMDProvisioning(context.Background(), record.Server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || info.AppID != 380870 || !info.Validate || info.TemplateID != "project-zomboid" || info.TemplateVersion != "2.0.0" {
		t.Fatalf("unexpected persisted steamcmd metadata: ok=%v info=%#v", ok, info)
	}

	other := testServer(t)
	other.Name = "no-steamcmd"
	otherRecord, err := service.CreateProvisioned(context.Background(), other, "custom-template", nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err = service.SteamCMDProvisioning(context.Background(), otherRecord.Server.ID); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("expected no steamcmd provisioning metadata when none was provided")
	}
}
