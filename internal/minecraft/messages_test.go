package minecraft

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamenode/internal/console"
	"gamenode/internal/database"
	"gamenode/internal/scheduler"
	"gamenode/internal/servers"
)

type captureConsoleInput struct {
	value  strings.Builder
	closed bool
}

func (w *captureConsoleInput) Write(p []byte) (int, error) { return w.value.Write(p) }
func (w *captureConsoleInput) Close() error                { w.closed = true; return nil }

func TestMessageSettingsValidateCustomWarningsAndMultipleMOTDs(t *testing.T) {
	settings := DefaultMessageSettings()
	settings.MOTDs = []MOTD{{ID: "welcome", Enabled: true, Interval: 300, Message: "Hello", Channel: "chat", Color: "aqua"}, {ID: "events", Enabled: true, Interval: 900, Message: "Events", Channel: "title", Color: "gold"}}
	if err := settings.Validate(); err != nil {
		t.Fatal(err)
	}
	settings.MOTDs[1].ID = settings.MOTDs[0].ID
	if err := settings.Validate(); !errors.Is(err, ErrInvalidMessages) {
		t.Fatalf("duplicate MOTD ID was accepted: %v", err)
	}
}

func TestNotificationJSONEscapingPreventsCommandInjection(t *testing.T) {
	session := console.NewManager().CreateSession("server", "instance")
	input := &captureConsoleInput{}
	session.AttachInput(input)
	message := `text " fake\n\"}],"run_command":"stop`
	if err := sendStyled(session, "chat", message, "gold", true, true, true, true, nil); err != nil {
		t.Fatal(err)
	}
	command := input.value.String()
	if !strings.HasPrefix(command, "tellraw @a ") || !strings.HasSuffix(command, "\n") {
		t.Fatalf("unexpected console command: %q", command)
	}
	if strings.Contains(command, "\n\"}],\"run_command\"") {
		t.Fatalf("message escaped its JSON component: %q", command)
	}
	for _, fragment := range []string{`"bold":true`, `"italic":true`, `"underlined":true`, `"strikethrough":true`, `\"run_command\"`} {
		if !strings.Contains(command, fragment) {
			t.Errorf("command missing %s: %q", fragment, command)
		}
	}
}

func TestConsoleInputUsesServerLineEnding(t *testing.T) {
	session := console.NewManager().CreateSession("server", "instance")
	input := &captureConsoleInput{}
	session.AttachInput(input)
	lineEnding := func(value string) string { return strings.ReplaceAll(value, "\n", "\r\n") }
	if err := sendStyled(session, "subtitle", "Restarting", "yellow", false, false, false, false, lineEnding); err != nil {
		t.Fatal(err)
	}
	got := input.value.String()
	if strings.Count(got, "\r\n") != 2 || strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Fatalf("line ending was not normalized: %q", got)
	}
	if !strings.Contains(got, `title @a title {"text":" "}`) {
		t.Fatalf("subtitle was not displayed with its title timing command: %q", got)
	}
}

func TestRenderMessageReplacesSupportedPlaceholders(t *testing.T) {
	got := renderMessage("{server_name}: {minutes} / {seconds} at {restart_time} ({reason})", "GameNode", 121, time.Date(2026, 10, 14, 18, 0, 0, 0, time.UTC))
	if got != "GameNode: 3 / 121 at 18:00 (scheduled restart)" {
		t.Fatalf("rendered=%q", got)
	}
}

type testScheduleLister []scheduler.Schedule

func (s testScheduleLister) ListEnabled(context.Context) ([]scheduler.Schedule, error) { return s, nil }

type testServerLister []servers.Record

func (s testServerLister) List(context.Context) ([]servers.Record, error) { return s, nil }

func TestMessageServicePersistsWarningAndRepeatingMOTDDelivery(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE minecraft_message_settings(server_id TEXT PRIMARY KEY,settings_json TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
		`CREATE TABLE minecraft_message_deliveries(server_id TEXT NOT NULL,delivery_key TEXT NOT NULL,delivered_at TEXT NOT NULL,PRIMARY KEY(server_id,delivery_key))`,
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	store := NewMessageStore(db)
	settings := MessageSettings{RestartWarnings: []RestartWarning{{ID: "5m", Enabled: true, Seconds: 300, Message: "Restart in {minutes}", Channel: "chat", Color: "gold"}}, MOTDs: []MOTD{{ID: "welcome", Enabled: true, Interval: 600, Message: "Welcome {server_name}", Channel: "chat", Color: "aqua"}}}
	if err = store.Save(context.Background(), "mc", settings); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 12, 5, 0, 0, time.UTC)
	schedules := testScheduleLister{{ID: "schedule", ServerID: "mc", Enabled: true, ScheduleType: scheduler.Daily, TimeOfDay: "12:10", TimeZone: "UTC"}}
	manager := console.NewManager()
	session := manager.CreateSession("mc", "process")
	input := &captureConsoleInput{}
	session.AttachInput(input)
	serverRecords := testServerLister{{Server: servers.Server{ID: "mc", Name: "Survival", Executable: "java"}, Runtime: servers.RuntimeState{CurrentState: servers.StateRunning}}}
	service := MessageService{Store: store, Schedules: schedules, Servers: serverRecords, Console: manager}
	if err = service.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	first := input.value.String()
	if strings.Count(first, "tellraw @a") != 2 || !strings.Contains(first, "Restart in 5") || !strings.Contains(first, "Welcome Survival") {
		t.Fatalf("initial delivery=%q", first)
	}
	if err = service.Tick(context.Background(), now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(input.value.String(), "tellraw @a") != 2 {
		t.Fatalf("duplicate restart warning or early MOTD: %q", input.value.String())
	}
	if err = service.Tick(context.Background(), now.Add(601*time.Second)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(input.value.String(), "tellraw @a") != 3 {
		t.Fatalf("MOTD did not repeat at configured interval: %q", input.value.String())
	}
}
