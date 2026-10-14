package minecraft

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type StartupStore struct{ db *sql.DB }

func NewStartupStore(db *sql.DB) *StartupStore { return &StartupStore{db: db} }

func DefaultStartupConfiguration() StartupConfiguration {
	return StartupConfiguration{Distribution: "vanilla", XmsMB: 1024, XmxMB: 4096, JVMArguments: []string{}, ProgramArguments: []string{"nogui"}}
}

func (s *StartupStore) Get(ctx context.Context, serverID string) (StartupConfiguration, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT configuration_json FROM minecraft_startup_configurations WHERE server_id=?`, serverID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultStartupConfiguration(), nil
	}
	if err != nil {
		return StartupConfiguration{}, err
	}
	var config StartupConfiguration
	if err = json.Unmarshal([]byte(raw), &config); err != nil {
		return StartupConfiguration{}, errors.New("persisted Minecraft startup configuration is invalid")
	}
	if config.JVMArguments == nil {
		config.JVMArguments = []string{}
	}
	if config.ProgramArguments == nil {
		config.ProgramArguments = []string{}
	}
	return config, nil
}

func (s *StartupStore) Save(ctx context.Context, serverID string, config StartupConfiguration) error {
	if config.Distribution == "" {
		config.Distribution = "vanilla"
	}
	if config.XmsMB <= 0 || config.XmxMB <= 0 || config.XmsMB > config.XmxMB {
		return errors.New("invalid Minecraft memory configuration")
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `INSERT INTO minecraft_startup_configurations(server_id,configuration_json,created_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET configuration_json=excluded.configuration_json,updated_at=excluded.updated_at`, serverID, string(raw), now, now)
	return err
}
