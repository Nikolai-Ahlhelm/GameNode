package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// File is the configuration selected at startup. Changes affect the next
// process start; live services retain their startup configuration.
type File struct {
	mu    sync.Mutex
	path  string
	value Config
}

func NewFile(path string, value Config) *File { return &File{path: path, value: value} }

func (f *File) Storage() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value.Data.Directory, f.value.Database.Path
}

func (f *File) SetStorage(dataDirectory, databasePath string) error {
	dataDirectory = filepath.Clean(strings.TrimSpace(dataDirectory))
	databasePath = filepath.Clean(strings.TrimSpace(databasePath))
	if !filepath.IsAbs(dataDirectory) || !filepath.IsAbs(databasePath) {
		return fmt.Errorf("data directory and database path must be absolute")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	next := f.value
	next.Data.Directory = dataDirectory
	next.Database.Path = databasePath
	contents, err := yaml.Marshal(next)
	if err != nil {
		return fmt.Errorf("serialize config: %w", err)
	}
	if err = os.WriteFile(f.path, contents, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	f.value = next
	return nil
}

type Config struct {
	Server struct {
		Listen          string `yaml:"listen"`
		TLSCert         string `yaml:"tls_cert"`
		TLSKey          string `yaml:"tls_key"`
		TrustLocalProxy bool   `yaml:"trust_local_proxy"`
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
	FTP struct {
		Enabled          bool   `yaml:"enabled"`
		Listen           string `yaml:"listen"`
		PublicHost       string `yaml:"public_host"`
		PassivePortStart int    `yaml:"passive_port_start"`
		PassivePortEnd   int    `yaml:"passive_port_end"`
		TLSCert          string `yaml:"tls_cert"`
		TLSKey           string `yaml:"tls_key"`
		RequireTLS       bool   `yaml:"require_tls"`
	} `yaml:"ftp"`
	Monitoring struct {
		SampleIntervalSeconds int `yaml:"sample_interval_seconds"`
		HistoryLimit          int `yaml:"history_limit"`
	} `yaml:"monitoring"`
}

func Default() Config {
	var c Config
	c.Server.Listen = "127.0.0.1:8443"
	c.Data.Directory = "./data"
	c.Database.Path = "./data/gamenode.db"
	c.Logging.Level = "info"
	c.Filesystem.MaxUploadBytes = 64 << 20
	c.FTP.Listen = "127.0.0.1:2121"
	c.FTP.PassivePortStart = 50000
	c.FTP.PassivePortEnd = 50100
	c.FTP.RequireTLS = true
	c.Monitoring.SampleIntervalSeconds = 5
	c.Monitoring.HistoryLimit = 300
	return c
}

func defaultForConfigPath(path string) (Config, error) {
	c := Default()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return c, fmt.Errorf("resolve config path: %w", err)
	}
	data := filepath.Join(filepath.Dir(absolute), "data")
	c.Data.Directory = data
	c.Database.Path = filepath.Join(data, "gamenode.db")
	return c, nil
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
	if c.FTP.Enabled {
		host, portText, splitErr := net.SplitHostPort(c.FTP.Listen)
		port, portErr := strconv.Atoi(portText)
		if splitErr != nil || host == "" || portErr != nil || port < 1 || port > 65535 {
			return c, fmt.Errorf("ftp.listen must be a host:port address")
		}
		if c.FTP.PublicHost != "" {
			ip := net.ParseIP(c.FTP.PublicHost)
			if ip == nil || ip.To4() == nil {
				return c, fmt.Errorf("ftp.public_host must be an IPv4 address")
			}
		}
		if c.FTP.PassivePortStart < 1 || c.FTP.PassivePortEnd > 65535 || c.FTP.PassivePortStart > c.FTP.PassivePortEnd || c.FTP.PassivePortEnd-c.FTP.PassivePortStart > 1000 {
			return c, fmt.Errorf("ftp passive port range must contain at most 1001 valid ports")
		}
		if (c.FTP.TLSCert == "") != (c.FTP.TLSKey == "") {
			return c, fmt.Errorf("ftp.tls_cert and ftp.tls_key must be configured together")
		}
		if c.FTP.RequireTLS && c.FTP.TLSCert == "" {
			return c, fmt.Errorf("ftp TLS certificate and key are required when ftp.require_tls is true")
		}
	}
	if c.Monitoring.SampleIntervalSeconds < 1 || c.Monitoring.SampleIntervalSeconds > 300 {
		return c, fmt.Errorf("monitoring.sample_interval_seconds must be between 1 and 300")
	}
	if c.Monitoring.HistoryLimit < 1 || c.Monitoring.HistoryLimit > 10000 {
		return c, fmt.Errorf("monitoring.history_limit must be between 1 and 10000")
	}
	return c, nil
}

// LoadOrCreate loads path or creates a complete default configuration there on
// first start. It never replaces an existing file.
func LoadOrCreate(path string) (Config, error) {
	if path == "" {
		return Default(), nil
	}
	c, err := Load(path)
	if err != nil {
		return c, err
	}
	if _, err = os.Stat(path); err == nil {
		return c, nil
	} else if !os.IsNotExist(err) {
		return c, fmt.Errorf("stat config: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return c, fmt.Errorf("create config directory: %w", err)
	}
	created, err := defaultForConfigPath(path)
	if err != nil {
		return c, err
	}
	contents, err := yaml.Marshal(created)
	if err != nil {
		return c, fmt.Errorf("serialize default config: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return Load(path)
	}
	if err != nil {
		return c, fmt.Errorf("create config: %w", err)
	}
	if _, err = file.Write(contents); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return c, fmt.Errorf("write default config: %w", err)
	}
	if closeErr != nil {
		return c, fmt.Errorf("close default config: %w", closeErr)
	}
	return created, nil
}

func (c Config) EnsureDirectories() error {
	if err := os.MkdirAll(c.Data.Directory, 0700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	return os.MkdirAll(filepath.Dir(c.Database.Path), 0700)
}
