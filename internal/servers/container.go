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
	Image             string              `json:"image"`
	ImageDigest       string              `json:"image_digest,omitempty"`
	Command           []string            `json:"command"`
	MemoryLimitBytes  int64               `json:"memory_limit_bytes"`
	CPULimitMillis    int                 `json:"cpu_limit_millis"`
	OwnershipToken    string              `json:"-"`
	StartupShell      string              `json:"startup_shell,omitempty"`
	StartupTemplate   string              `json:"startup_template,omitempty"`
	EggSnapshot       *EggRuntimeSnapshot `json:"egg_snapshot,omitempty"`
	PIDsLimit         int64               `json:"pids_limit,omitempty"`
	TmpfsSizeBytes    int64               `json:"tmpfs_size_bytes,omitempty"`
	ImageAvailability string              `json:"image_availability,omitempty"`
	PullState         string              `json:"pull_state,omitempty"`
}

// EggRuntimeSnapshot is immutable provenance/configuration captured at
// provisioning. It contains no resolved secret values or raw Egg JSON.
type EggRuntimeSnapshot struct {
	SourceType          string                             `json:"source_type"`
	SourceIdentifier    string                             `json:"source_identifier,omitempty"`
	SourceHash          string                             `json:"source_hash,omitempty"`
	SourceFormatVersion string                             `json:"source_format_version,omitempty"`
	TemplateVersion     string                             `json:"template_version,omitempty"`
	SelectedImage       string                             `json:"selected_image"`
	ImageDigest         string                             `json:"image_digest,omitempty"`
	StartupTemplate     string                             `json:"startup_template"`
	StartupShell        string                             `json:"startup_shell"`
	VariableSensitivity map[string]bool                    `json:"variable_sensitivity"`
	Ports               []ContainerPortSnapshot            `json:"ports,omitempty"`
	ResourceDefaults    ContainerResourceSnapshot          `json:"resource_defaults"`
	ConfigOperations    []ContainerConfigSnapshotOperation `json:"config_operations,omitempty"`
}

type ContainerPortSnapshot struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
}

type ContainerResourceSnapshot struct {
	MemoryLimitBytes int64 `json:"memory_limit_bytes"`
	CPULimitMillis   int   `json:"cpu_limit_millis"`
	PIDsLimit        int64 `json:"pids_limit"`
	TmpfsSizeBytes   int64 `json:"tmpfs_size_bytes"`
}

type ContainerConfigSnapshotOperation struct {
	Format   string `json:"format"`
	Target   string `json:"target"`
	Key      string `json:"key"`
	Property string `json:"property,omitempty"`
	Required bool   `json:"required,omitempty"`
}

var imageReference = regexp.MustCompile(`^(?:(?:[a-z0-9](?:[a-z0-9.-]{0,62}[a-z0-9])?)(?::[0-9]{1,5})?/)?(?:[a-z0-9][a-z0-9._-]{0,127}/)*[a-z0-9](?:[a-z0-9._-]{0,127})(?::[A-Za-z0-9][A-Za-z0-9_.-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)
var containerPlaceholder = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}|\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

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
	if c.PIDsLimit < 0 || c.PIDsLimit > 32768 || c.TmpfsSizeBytes < 0 || c.TmpfsSizeBytes > 1<<30 {
		return errors.New("container sandbox limits are invalid")
	}
	if c.StartupShell != "" && c.StartupShell != "sh" && c.StartupShell != "bash" && c.StartupShell != "/bin/sh" && c.StartupShell != "/bin/bash" {
		return errors.New("container startup shell is not allowed")
	}
	if c.StartupTemplate != "" && c.StartupShell == "" {
		c.StartupShell = "/bin/sh"
	}
	if len(c.StartupTemplate) > 64<<10 || strings.ContainsRune(c.StartupTemplate, 0) {
		return errors.New("container startup template is invalid")
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
	return saveContainerConfigExec(ctx, store.db, serverID, config)
}

func (store *Store) saveContainerConfigTx(ctx context.Context, tx *sql.Tx, serverID string, config *ContainerConfig) error {
	return saveContainerConfigExec(ctx, tx, serverID, config)
}

type execContexter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func saveContainerConfigExec(ctx context.Context, execer execContexter, serverID string, config *ContainerConfig) error {
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
	snapshot, _ := json.Marshal(config.EggSnapshot)
	_, err := execer.ExecContext(ctx, `INSERT INTO server_container_configs(server_id,image,image_digest,command_json,memory_limit_bytes,cpu_limit_millis,ownership_token,egg_snapshot_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET image=excluded.image,image_digest=excluded.image_digest,command_json=excluded.command_json,memory_limit_bytes=excluded.memory_limit_bytes,cpu_limit_millis=excluded.cpu_limit_millis,ownership_token=excluded.ownership_token,egg_snapshot_json=excluded.egg_snapshot_json,updated_at=excluded.updated_at`, serverID, config.Image, config.ImageDigest, string(command), config.MemoryLimitBytes, config.CPULimitMillis, config.OwnershipToken, string(snapshot), now, now)
	return err
}

func (store *Store) containerConfig(ctx context.Context, serverID string) (*ContainerConfig, error) {
	var config ContainerConfig
	var command, snapshot string
	err := store.db.QueryRowContext(ctx, `SELECT image,image_digest,command_json,memory_limit_bytes,cpu_limit_millis,ownership_token,egg_snapshot_json FROM server_container_configs WHERE server_id=?`, serverID).Scan(&config.Image, &config.ImageDigest, &command, &config.MemoryLimitBytes, &config.CPULimitMillis, &config.OwnershipToken, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(command), &config.Command) != nil {
		return nil, errors.New("invalid persisted container configuration")
	}
	if snapshot != "" && snapshot != "null" {
		if json.Unmarshal([]byte(snapshot), &config.EggSnapshot) != nil {
			return nil, errors.New("invalid persisted Egg runtime snapshot")
		}
		if config.EggSnapshot != nil {
			config.StartupShell = config.EggSnapshot.StartupShell
			config.StartupTemplate = config.EggSnapshot.StartupTemplate
			config.ImageDigest = config.EggSnapshot.ImageDigest
			config.PIDsLimit = config.EggSnapshot.ResourceDefaults.PIDsLimit
			config.TmpfsSizeBytes = config.EggSnapshot.ResourceDefaults.TmpfsSizeBytes
		}
	}
	return &config, nil
}

// expandContainerCommand performs only the two declared placeholder forms.
// Shell expansion remains inside the container and no host environment is
// consulted. The function is intentionally local to the container runtime
// model rather than a general command/template engine.
func expandContainerCommand(command []string, environment map[string]string) ([]string, error) {
	values := make(map[string]string, len(environment)+1)
	for key, value := range environment {
		values[key] = value
	}
	values["SERVER_ROOT"] = ContainerMount
	result := make([]string, len(command))
	for index, value := range command {
		if strings.ContainsRune(value, 0) {
			return nil, errors.New("container startup contains an invalid value")
		}
		var expansionErr error
		result[index] = containerPlaceholder.ReplaceAllStringFunc(value, func(token string) string {
			match := containerPlaceholder.FindStringSubmatch(token)
			key := match[1]
			if key == "" {
				key = match[2]
			}
			value, ok := values[key]
			if !ok {
				expansionErr = errors.New("container startup references an unknown variable")
				return ""
			}
			return value
		})
		if expansionErr != nil {
			return nil, expansionErr
		}
	}
	return result, nil
}
