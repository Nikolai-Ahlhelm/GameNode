package servers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	RuntimeNative    = "native"
	RuntimeContainer = "container"
	ContainerMount   = "/home/container"
)

// ContainerConfig is deliberately a closed GameNode model: there are no
// engine flags, extra mounts, privileged mode, host namespaces, devices, or
// registry credentials to pass through from a request.
type ContainerConfig struct {
	Image             string   `json:"image"`
	ImageDigest       string   `json:"image_digest,omitempty"`
	Command           []string `json:"command"`
	MemoryLimitBytes  int64    `json:"memory_limit_bytes"`
	CPULimitMillis    int      `json:"cpu_limit_millis"`
	OwnershipToken    string   `json:"-"`
	ImageAvailability string   `json:"image_availability,omitempty"`
	PullState         string   `json:"pull_state,omitempty"`
}

var imageReference = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9._-]{0,127})/)*[a-z0-9](?:[a-z0-9._-]{0,127})(?::[A-Za-z0-9][A-Za-z0-9_.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)

func (c *ContainerConfig) Validate() error {
	c.Image = strings.TrimSpace(c.Image)
	if len(c.Image) == 0 || len(c.Image) > 512 || !imageReference.MatchString(c.Image) {
		return errors.New("invalid container image reference")
	}
	if c.ImageDigest != "" && !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(c.ImageDigest) {
		return errors.New("invalid container image digest")
	}
	if c.MemoryLimitBytes < 16<<20 || c.MemoryLimitBytes > 1<<50 {
		return errors.New("container memory limit must be between 16 MiB and 1 PiB")
	}
	if c.CPULimitMillis < 10 || c.CPULimitMillis > 1_000_000 {
		return errors.New("container CPU limit must be between 10 and 1000000 millicores")
	}
	if len(c.Command) > 128 {
		return errors.New("too many container command arguments")
	}
	for _, value := range c.Command {
		if len(value) > 4096 || strings.ContainsRune(value, 0) {
			return errors.New("invalid container command argument")
		}
	}
	return nil
}

func (store *Store) saveContainerConfig(ctx context.Context, serverID string, config *ContainerConfig) error {
	if config == nil {
		return errors.New("container configuration is required")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if config.OwnershipToken == "" {
		value, err := newID()
		if err != nil {
			return err
		}
		config.OwnershipToken = value
	}
	command, _ := json.Marshal(config.Command)
	now := stamp(time.Now().UTC())
	_, err := store.db.ExecContext(ctx, `INSERT INTO server_container_configs(server_id,image,image_digest,command_json,memory_limit_bytes,cpu_limit_millis,ownership_token,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET image=excluded.image,image_digest=excluded.image_digest,command_json=excluded.command_json,memory_limit_bytes=excluded.memory_limit_bytes,cpu_limit_millis=excluded.cpu_limit_millis,updated_at=excluded.updated_at`, serverID, config.Image, config.ImageDigest, string(command), config.MemoryLimitBytes, config.CPULimitMillis, config.OwnershipToken, now, now)
	return err
}

func (store *Store) containerConfig(ctx context.Context, serverID string) (*ContainerConfig, error) {
	var config ContainerConfig
	var command string
	err := store.db.QueryRowContext(ctx, `SELECT image,image_digest,command_json,memory_limit_bytes,cpu_limit_millis,ownership_token FROM server_container_configs WHERE server_id=?`, serverID).Scan(&config.Image, &config.ImageDigest, &command, &config.MemoryLimitBytes, &config.CPULimitMillis, &config.OwnershipToken)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(command), &config.Command) != nil {
		return nil, errors.New("invalid persisted container configuration")
	}
	return &config, nil
}
