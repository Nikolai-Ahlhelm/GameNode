package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Listen  string `yaml:"listen"`
		TLSCert string `yaml:"tls_cert"`
		TLSKey  string `yaml:"tls_key"`
	} `yaml:"server"`
	Data struct {
		Directory string `yaml:"directory"`
	} `yaml:"data"`
	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`
	Logging struct {
		Level string `yaml:"level"`
	} `yaml:"logging"`
	Filesystem struct {
		MaxUploadBytes int64 `yaml:"max_upload_bytes"`
	} `yaml:"filesystem"`
}

func Default() Config {
	var c Config
	c.Server.Listen = "127.0.0.1:8443"
	c.Data.Directory = "./data"
	c.Database.Path = "./data/gamenode.db"
	c.Logging.Level = "info"
	c.Filesystem.MaxUploadBytes = 64 << 20
	return c
}

func Load(path string) (Config, error) {
	c := Default()
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.Listen == "" || c.Database.Path == "" {
		return c, fmt.Errorf("server.listen and database.path are required")
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		return c, fmt.Errorf("server.tls_cert and server.tls_key must be configured together")
	}
	if c.Filesystem.MaxUploadBytes < 1<<20 {
		return c, fmt.Errorf("filesystem.max_upload_bytes must be at least 1 MiB")
	}
	return c, nil
}

func (c Config) EnsureDirectories() error {
	if err := os.MkdirAll(c.Data.Directory, 0700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return os.MkdirAll(filepath.Dir(c.Database.Path), 0700)
}
