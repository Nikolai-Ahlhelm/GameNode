package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"time"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/config"
	"gamenode/internal/console"
	"gamenode/internal/database"
	"gamenode/internal/diagnostics"
	"gamenode/internal/filesystem"
	"gamenode/internal/gameconfig"
	"gamenode/internal/logging"
	"gamenode/internal/monitoring"
	"gamenode/internal/provisioning"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
	"gamenode/internal/settings"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
)

//go:embed webassets
var webAssets embed.FS

func main() {
	configPath := flag.String("config", "", "Path to YAML configuration (defaults to config.yaml beside the executable)")
	flag.Parse()
	path := *configPath
	if path == "" {
		executable, pathErr := os.Executable()
		if pathErr != nil {
			fmt.Fprintln(os.Stderr, "configuration path error:", pathErr)
			os.Exit(1)
		}
		path = filepath.Join(filepath.Dir(executable), "config.yaml")
	}
	cfg, err := config.LoadOrCreate(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(1)
	}
	configFile := config.NewFile(path, cfg)
	if err = cfg.EnsureDirectories(); err != nil {
		fmt.Fprintln(os.Stderr, "data directory error:", err)
		os.Exit(1)
	}
	logManager, log, err := logging.New(filepath.Join(cfg.Data.Directory, "log"), cfg.Logging.Level)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logging error:", err)
		os.Exit(1)
	}
	db, err := database.Open(cfg.Database.Path)
	if err != nil {
		log.Error("open database failed", "error", err.Error())
		os.Exit(1)
	}
	defer db.Close()
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		log.Error("migration failed", "error", err.Error())
		os.Exit(1)
	}
	settingService := settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: cfg.Monitoring.SampleIntervalSeconds, MonitoringHistoryLimit: cfg.Monitoring.HistoryLimit, LoggingLevel: cfg.Logging.Level})
	currentSettings, err := settingService.Get(context.Background())
	if err != nil {
		log.Error("load persisted settings failed", "error", err.Error())
		os.Exit(1)
	}
	if err = logManager.SetLevel(currentSettings.Logging.Level); err != nil {
		log.Error("invalid persisted logging level", "module", "Settings.Logging", "error", err.Error())
		os.Exit(1)
	}
	settingService.SetOnUpdate(func(values settings.Values, changed []string) {
		for _, field := range changed {
			if field == "logging.level" {
				if setErr := logManager.SetLevel(values.Logging.Level); setErr != nil {
					log.Error("logging level could not be applied", "module", "Settings.Logging", "error", setErr.Error())
				}
			}
		}
	})
	assets, err := fs.Sub(webAssets, "webassets")
	if err != nil {
		log.Error("embedded frontend unavailable", "error", err.Error())
		os.Exit(1)
	}
	static := spaHandler(assets)
	transportTLS := cfg.Server.TLSCert != ""
	secureCookie := transportTLS || cfg.Server.TrustLocalProxy
	serverService := servers.NewServiceWithMonitoring(servers.NewStore(db), runtime.NewNative(), console.NewManager(), monitoring.Options{Interval: time.Duration(currentSettings.Monitoring.SampleIntervalSeconds) * time.Second, HistoryLimit: currentSettings.Monitoring.HistoryLimit})
	if err = serverService.Rediscover(context.Background()); err != nil {
		log.Error("server rediscovery failed", "error", err.Error())
	}
	files := filesystem.New(filesystem.Options{MaxUploadBytes: cfg.Filesystem.MaxUploadBytes})
	diagnosticService := diagnostics.New(db, settingService, diagnostics.MonitoringEffective{SampleIntervalSeconds: currentSettings.Monitoring.SampleIntervalSeconds, HistoryLimit: currentSettings.Monitoring.HistoryLimit}, time.Now().UTC())
	catalog := templates.NewCatalogManager(templates.NewOfficialHTTPSource(), cfg.Data.Directory, diagnostics.Version)
	templateService := templates.NewServiceWithCatalog(templates.NewStore(db), catalog)
	steamPlatform, err := steamcmd.CurrentPlatform(goRuntime.GOOS)
	if err != nil {
		log.Error("SteamCMD platform unavailable", "error", err.Error())
		os.Exit(1)
	}
	steamManager := steamcmd.New(filepath.Join(cfg.Data.Directory, "tools", "steamcmd"), steamPlatform, nil, nil)
	provisioner := provisioning.NewWithOptions(db, templateService, steamManager, serverService, cfg.Data.Directory, provisioning.Options{Log: log})
	gameConfigService := gameconfig.New(db, serverService)
	defer provisioner.Close()
	if err = provisioner.Initialize(context.Background()); err != nil {
		log.Error("provisioning recovery failed", "error", err.Error())
		os.Exit(1)
	}
	handler := api.New(auth.New(db), serverService, log, secureCookie, api.Options{TrustLocalProxy: cfg.Server.TrustLocalProxy, Filesystem: files, Settings: settingService, Diagnostics: diagnosticService, Templates: templateService, Provisioning: provisioner, GameConfig: gameConfigService, Logs: logManager, SetupConfig: configFile, SteamCMD: steamManager}).Handler(static)
	server := &http.Server{Addr: cfg.Server.Listen, Handler: handler, ReadHeaderTimeout: 0, ReadTimeout: 15e9, WriteTimeout: 15e9, IdleTimeout: 60e9}
	log.Info("GameNode starting", "listen", cfg.Server.Listen, "tls", transportTLS, "trust_local_proxy", cfg.Server.TrustLocalProxy)
	if transportTLS {
		err = server.ListenAndServeTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && err != http.ErrServerClosed {
		log.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}
func spaHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if _, err := fs.Stat(assets, name); err == nil {
				http.FileServer(http.FS(assets)).ServeHTTP(w, r)
				return
			}
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = "/"
		http.FileServer(http.FS(assets)).ServeHTTP(w, clone)
	})
}
