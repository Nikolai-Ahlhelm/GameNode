package minecraft

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"gamenode/internal/console"
	"gamenode/internal/scheduler"
	"gamenode/internal/servers"
)

// MessageSettings is deliberately Minecraft-specific. Multiple MOTDs may be
// active at once; each has its own interval, text, channel and visual style.
type MessageSettings struct {
	RestartWarnings []RestartWarning `json:"restart_warnings"`
	MOTDs           []MOTD           `json:"motds"`
}
type RestartWarning struct {
	ID            string `json:"id"`
	Enabled       bool   `json:"enabled"`
	Seconds       int    `json:"seconds"`
	Message       string `json:"message"`
	Channel       string `json:"channel"`
	Color         string `json:"color"`
	Bold          bool   `json:"bold"`
	Italic        bool   `json:"italic"`
	Underline     bool   `json:"underlined"`
	Strikethrough bool   `json:"strikethrough"`
}
type MOTD struct {
	ID            string `json:"id"`
	Enabled       bool   `json:"enabled"`
	Interval      int    `json:"interval_seconds"`
	Message       string `json:"message"`
	Channel       string `json:"channel"`
	Color         string `json:"color"`
	Bold          bool   `json:"bold"`
	Italic        bool   `json:"italic"`
	Underline     bool   `json:"underlined"`
	Strikethrough bool   `json:"strikethrough"`
}

var ErrInvalidMessages = errors.New("invalid Minecraft message settings")

func DefaultMessageSettings() MessageSettings {
	return MessageSettings{
		RestartWarnings: []RestartWarning{
			{ID: "15m", Enabled: true, Seconds: 900, Message: "Server restarting in {minutes} minutes!", Channel: "chat", Color: "gold"},
			{ID: "5m", Enabled: true, Seconds: 300, Message: "Server restarting in {minutes} minutes!", Channel: "chat", Color: "gold"},
			{ID: "3m", Enabled: true, Seconds: 180, Message: "Server restarting in {minutes} minutes!", Channel: "chat", Color: "gold"},
			{ID: "2m", Enabled: true, Seconds: 120, Message: "Server restarting in {minutes} minutes!", Channel: "chat", Color: "gold"},
			{ID: "1m", Enabled: true, Seconds: 60, Message: "Server restarting in {minutes} minute!", Channel: "chat", Color: "gold"},
		},
		MOTDs: []MOTD{},
	}
}

func (s MessageSettings) Validate() error {
	if len(s.RestartWarnings) > 32 || len(s.MOTDs) > 32 {
		return fmt.Errorf("%w: too many messages", ErrInvalidMessages)
	}
	seen := map[string]bool{}
	for _, w := range s.RestartWarnings {
		if !validID(w.ID) || seen["w:"+w.ID] || w.Seconds < 1 || w.Seconds > 7*24*3600 || len(w.Message) > 1000 || strings.TrimSpace(w.Message) == "" || !validChannel(w.Channel) || !validColor(w.Color) {
			return fmt.Errorf("%w: invalid restart warning", ErrInvalidMessages)
		}
		seen["w:"+w.ID] = true
	}
	for _, m := range s.MOTDs {
		if !validID(m.ID) || seen["m:"+m.ID] || m.Interval < 10 || m.Interval > 365*24*3600 || len(m.Message) > 1000 || strings.TrimSpace(m.Message) == "" || !validChannel(m.Channel) || !validColor(m.Color) {
			return fmt.Errorf("%w: invalid MOTD", ErrInvalidMessages)
		}
		seen["m:"+m.ID] = true
	}
	return nil
}
func validID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func validChannel(s string) bool {
	return s == "chat" || s == "title" || s == "subtitle" || s == "chat_title"
}
func validColor(s string) bool {
	if s == "" {
		return true
	}
	switch s {
	case "black", "dark_blue", "dark_green", "dark_aqua", "dark_red", "dark_purple", "gold", "gray", "dark_gray", "blue", "green", "aqua", "red", "light_purple", "yellow", "white":
		return true
	}
	return false
}

type MessageStore struct{ db *sql.DB }

func NewMessageStore(db *sql.DB) *MessageStore { return &MessageStore{db: db} }
func (s *MessageStore) Get(ctx context.Context, serverID string) (MessageSettings, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT settings_json FROM minecraft_message_settings WHERE server_id=?`, serverID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultMessageSettings(), nil
	}
	if err != nil {
		return MessageSettings{}, err
	}
	var v MessageSettings
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return MessageSettings{}, errors.New("persisted Minecraft messages are invalid")
	}
	if v.RestartWarnings == nil {
		v.RestartWarnings = []RestartWarning{}
	}
	if v.MOTDs == nil {
		v.MOTDs = []MOTD{}
	}
	return v, nil
}

func (s *MessageStore) HasSettings(ctx context.Context, serverID string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM minecraft_message_settings WHERE server_id=?`, serverID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
func (s *MessageStore) Save(ctx context.Context, serverID string, v MessageSettings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `INSERT INTO minecraft_message_settings(server_id,settings_json,created_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET settings_json=excluded.settings_json,updated_at=excluded.updated_at`, serverID, string(raw), now, now)
	return err
}
func (s *MessageStore) Delivered(ctx context.Context, serverID, key string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM minecraft_message_deliveries WHERE server_id=? AND delivery_key=?`, serverID, key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
func (s *MessageStore) MarkDelivered(ctx context.Context, serverID, key string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO minecraft_message_deliveries(server_id,delivery_key,delivered_at) VALUES(?,?,?)`, serverID, key, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func (s *MessageStore) LastMOTD(ctx context.Context, serverID, id string) (time.Time, bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT delivered_at FROM minecraft_message_deliveries WHERE server_id=? AND delivery_key=?`, serverID, "motd:"+id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, e := time.Parse(time.RFC3339Nano, raw)
	return t, true, e
}
func (s *MessageStore) MarkMOTD(ctx context.Context, serverID, id string, deliveredAt time.Time) error {
	key := "motd:" + id
	now := deliveredAt.UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO minecraft_message_deliveries(server_id,delivery_key,delivered_at) VALUES(?,?,?) ON CONFLICT(server_id,delivery_key) DO UPDATE SET delivered_at=excluded.delivered_at`, serverID, key, now)
	return err
}

type ScheduleLister interface {
	ListEnabled(context.Context) ([]scheduler.Schedule, error)
}
type ServerLister interface {
	List(context.Context) ([]servers.Record, error)
}
type MessageService struct {
	Store     *MessageStore
	Schedules ScheduleLister
	Servers   ServerLister
	Console   *console.Manager
	Interval  time.Duration
	Log       *slog.Logger
}

// Run checks absolute schedule times instead of sleeping until a milestone.
// Its delivery keys are persisted, so a backend restart cannot repeat a warning.
func (m *MessageService) Run(ctx context.Context) {
	d := m.Interval
	if d <= 0 {
		d = time.Second
	}
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		if err := m.Tick(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			m.logger().Warn("Minecraft message delivery pass failed", "module", "MinecraftMessages", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *MessageService) Tick(ctx context.Context, now time.Time) error {
	if m.Store == nil || m.Schedules == nil || m.Servers == nil || m.Console == nil {
		return errors.New("Minecraft messaging service is unavailable")
	}
	schedules, err := m.Schedules.ListEnabled(ctx)
	if err != nil {
		return err
	}
	records, err := m.Servers.List(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]servers.Record, len(records))
	for _, r := range records {
		byID[r.Server.ID] = r
	}
	for id, r := range byID {
		hasSettings, e := m.Store.HasSettings(ctx, id)
		if e != nil {
			return e
		}
		if !hasSettings {
			continue
		}
		if r.Runtime.CurrentState != servers.StateRunning {
			continue
		}
		settings, e := m.Store.Get(ctx, id)
		if e != nil {
			return e
		}
		session, ok := m.Console.CurrentSession(id)
		if !ok || session == nil {
			continue
		}
		for _, motd := range settings.MOTDs {
			if !motd.Enabled {
				continue
			}
			last, exists, e := m.Store.LastMOTD(ctx, id, motd.ID)
			if e != nil {
				return e
			}
			if exists && now.Sub(last) < time.Duration(motd.Interval)*time.Second {
				continue
			}
			message := strings.ReplaceAll(motd.Message, "{server_name}", r.Server.Name)
			if e = sendStyled(session, motd.Channel, message, motd.Color, motd.Bold, motd.Italic, motd.Underline, motd.Strikethrough, r.Server.ConsoleInput); e == nil {
				if e = m.Store.MarkMOTD(ctx, id, motd.ID, now); e != nil {
					return e
				}
			} else {
				m.logger().Warn("Minecraft MOTD could not be delivered", "module", "MinecraftMessages", "server_id", id, "motd_id", motd.ID, "error", e)
			}
		}
		for _, sch := range schedules {
			if sch.ServerID != id {
				continue
			}
			next, e := scheduler.NextOccurrence(now, sch)
			if e != nil {
				continue
			}
			remaining := int(next.Sub(now).Seconds())
			for _, warning := range settings.RestartWarnings {
				if !warning.Enabled || remaining > warning.Seconds || remaining < warning.Seconds-2 {
					continue
				}
				key := fmt.Sprintf("restart:%s:%s:%s", sch.ID, next.UTC().Format(time.RFC3339), warning.ID)
				delivered, e := m.Store.Delivered(ctx, id, key)
				if e != nil {
					return e
				}
				if delivered {
					continue
				}
				text := renderMessage(warning.Message, r.Server.Name, remaining, next)
				if e = sendStyled(session, warning.Channel, text, warning.Color, warning.Bold, warning.Italic, warning.Underline, warning.Strikethrough, r.Server.ConsoleInput); e == nil {
					if e = m.Store.MarkDelivered(ctx, id, key); e != nil {
						return e
					}
				} else {
					m.logger().Warn("Minecraft restart warning could not be delivered", "module", "MinecraftMessages", "server_id", id, "schedule_id", sch.ID, "warning_id", warning.ID, "error", e)
				}
			}
		}
	}
	return nil
}

func (m *MessageService) logger() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func renderMessage(t, name string, seconds int, at time.Time) string {
	t = strings.ReplaceAll(t, "{server_name}", name)
	t = strings.ReplaceAll(t, "{minutes}", fmt.Sprint((seconds+59)/60))
	t = strings.ReplaceAll(t, "{seconds}", fmt.Sprint(seconds))
	t = strings.ReplaceAll(t, "{restart_time}", at.Format("15:04"))
	t = strings.ReplaceAll(t, "{reason}", "scheduled restart")
	return t
}
func sendStyled(s *console.Session, channel, message, color string, bold, italic, underlined, strikethrough bool, lineEnding func(string) string) error {
	component := map[string]any{"text": message}
	if color != "" {
		component["color"] = color
	}
	if bold {
		component["bold"] = true
	}
	if italic {
		component["italic"] = true
	}
	if underlined {
		component["underlined"] = true
	}
	if strikethrough {
		component["strikethrough"] = true
	}
	data, err := json.Marshal(component)
	if err != nil {
		return err
	}
	commands := []string{}
	switch channel {
	case "chat":
		commands = append(commands, "tellraw @a "+string(data))
	case "title":
		commands = append(commands, "title @a title "+string(data))
	case "subtitle":
		commands = append(commands, "title @a subtitle "+string(data), `title @a title {"text":" "}`)
	case "chat_title":
		commands = append(commands, "tellraw @a "+string(data), "title @a title "+string(data))
	}
	for _, cmd := range commands {
		input := cmd + "\n"
		if lineEnding != nil {
			input = lineEnding(input)
		}
		if err = s.Input(input); err != nil {
			return err
		}
	}
	return nil
}
