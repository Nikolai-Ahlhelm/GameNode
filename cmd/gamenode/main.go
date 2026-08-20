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
	"gamenode/internal/audit"
	"gamenode/internal/auth"
	"gamenode/internal/config"
	"gamenode/internal/console"
	"gamenode/internal/database"
	"gamenode/internal/diagnostics"
	"gamenode/internal/emailverification"
	"gamenode/internal/filesystem"
	ftpservice "gamenode/internal/ftp"
	"gamenode/internal/gameconfig"
	"gamenode/internal/logging"
	"gamenode/internal/monitoring"
	"gamenode/internal/notifications"
	"gamenode/internal/provisioning"
	"gamenode/internal/runtime"
	"gamenode/internal/scheduler"
	"gamenode/internal/servers"
	"gamenode/internal/serverupdates"
	"gamenode/internal/settings"
	"gamenode/internal/statushistory"
	"gamenode/internal/steamcmd"
	"gamenode/internal/templates"
)

//go:embed webassets
var webAssets embed.FS

func main() {
	// A disposable console-signal helper invocation never performs normal
	// GameNode startup. See internal/runtime.RunConsoleSignalHelper and
	// docs/runtime.md for why this compiled binary re-execs itself instead of
	// shelling out to a script or introducing a second service.
	if code, ok := runtime.RunConsoleSignalHelper(); ok {
		os.Exit(code)
	}
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
	log.Info("application initialization started", "module", "Application", "version", diagnostics.Version, "platform", goRuntime.GOOS)
	log.Info("opening database", "module", "Database", "category", logging.CategoryDatabase)
	db, err := database.Open(cfg.Database.Path)
	if err != nil {
		log.Error("open database failed", "category", logging.CategoryDatabase, "error", err.Error())
		os.Exit(1)
	}
	defer db.Close()
	log.Info("database opened", "module", "Database", "category", logging.CategoryDatabase)
	log.Info("checking database migrations", "module", "Database.Migration", "category", logging.CategoryDatabase)
	backupPath, pendingMigrations, err := database.BackupIfMigrationPending(db, cfg.Database.Path, gamenode.MigrationFiles)
	if err != nil {
		log.Error("migration backup failed", "category", logging.CategoryDatabase, "error", err.Error())
		os.Exit(1)
	}
	if pendingMigrations && backupPath != "" {
		log.Info("created database backup before migration", "category", logging.CategoryDatabase, "path", backupPath)
	}
	if err = database.Migrate(db, gamenode.MigrationFiles); err != nil {
		log.Error("migration failed", "category", logging.CategoryDatabase, "error", err.Error())
		os.Exit(1)
	}
	log.Info("database migrations completed", "module", "Database.Migration", "category", logging.CategoryDatabase)
	settingService := settings.New(db, settings.Defaults{MonitoringSampleIntervalSeconds: cfg.Monitoring.SampleIntervalSeconds, MonitoringHistoryLimit: cfg.Monitoring.HistoryLimit, LoggingLevel: cfg.Logging.Level})
	currentSettings, err := settingService.Get(context.Background())
	if err != nil {
		log.Error("load persisted settings failed", "error", err.Error())
		os.Exit(1)
	}
	log.Info("persisted settings loaded", "module", "Settings", "monitoring_interval_seconds", currentSettings.Monitoring.SampleIntervalSeconds, "monitoring_history_limit", currentSettings.Monitoring.HistoryLimit, "logging_level", currentSettings.Logging.Level, "logging_detailed_errors", currentSettings.Logging.DetailedErrors)
	if err = logManager.SetLevel(currentSettings.Logging.Level); err != nil {
		log.Error("invalid persisted logging level", "module", "Settings.Logging", "error", err.Error())
		os.Exit(1)
	}
	if err = logManager.SetCategories(currentSettings.Logging.Categories.AsMap()); err != nil {
		log.Error("invalid persisted logging categories", "module", "Settings.Logging", "error", err.Error())
		os.Exit(1)
	}
	logManager.SetDetailedErrors(currentSettings.Logging.DetailedErrors)
	// applyLoggingSettings re-syncs the live logger from the full settings
	// snapshot on every update, not just when a logging field is reported as
	// changed - simpler than tracking each field name and just as cheap.
	applyLoggingSettings := func(values settings.Values) {
		if setErr := logManager.SetLevel(values.Logging.Level); setErr != nil {
			log.Error("logging level could not be applied", "module", "Settings.Logging", "error", setErr.Error())
		}
		if setErr := logManager.SetCategories(values.Logging.Categories.AsMap()); setErr != nil {
			log.Error("logging categories could not be applied", "module", "Settings.Logging", "error", setErr.Error())
		}
		logManager.SetDetailedErrors(values.Logging.DetailedErrors)
	}
	var provisioner *provisioning.Service
	settingService.SetOnUpdate(func(values settings.Values, changed []string) {
		applyLoggingSettings(values)
		if provisioner != nil {
			provisioner.SetImagePolicy(provisioning.ImagePolicy{AllowedRegistries: values.Runtime.ContainerImageAllowlist})
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
	dockerEngine := runtime.NewDockerEngine()
	serverService := servers.NewServiceWithMonitoring(servers.NewStore(db), runtime.NewHybridWithEngine(dockerEngine), console.NewManager(), monitoring.Options{Interval: time.Duration(currentSettings.Monitoring.SampleIntervalSeconds) * time.Second, HistoryLimit: currentSettings.Monitoring.HistoryLimit})
	serverService.SetLogger(logging.WithCategory(log, logging.CategoryRuntime))
	emailAlerts := notifications.New(db, logging.WithCategory(log, logging.CategoryGeneral))
	defer emailAlerts.Close()
	emailVerification := emailverification.New(db, emailAlerts)
	serverService.SetLifecycleObserver(func(event servers.LifecycleEvent) {
		emailAlerts.Enqueue(notifications.Event{Type: event.Type, ServerID: event.ServerID, ServerName: event.ServerName, TenantID: event.TenantID, ExitCode: event.ExitCode, OccurredAt: event.OccurredAt})
	})
	if err = serverService.Rediscover(context.Background()); err != nil {
		log.Error("server rediscovery failed", "error", err.Error())
	}
	statusHistory := statushistory.New(db)
	statusHistoryContext, stopStatusHistory := context.WithCancel(context.Background())
	defer stopStatusHistory()
	recordStatusHistory := func(ctx context.Context) {
		now := time.Now().UTC().Truncate(statushistory.Interval)
		records, listErr := serverService.List(ctx)
		if listErr != nil {
			log.Error("status history server listing failed", "module", "StatusHistory", "error", listErr.Error())
			return
		}
		checks := make([]statushistory.Check, 0, len(records))
		for _, record := range records {
			snapshot, snapshotErr := serverService.MonitoringSnapshot(ctx, record.Server.ID)
			health := monitoring.HealthUnknown
			if snapshotErr == nil {
				health = snapshot.Health
			}
			checks = append(checks, statushistory.Check{ServerID: record.Server.ID, CheckedAt: now, Status: statushistory.StatusFromHealth(health), State: record.Runtime.CurrentState})
		}
		if err := statusHistory.RecordBatch(ctx, checks); err != nil {
			log.Error("status history write failed", "module", "StatusHistory", "error", err.Error())
		}
		if err := statusHistory.PruneBefore(ctx, now.Add(-statushistory.Retention)); err != nil {
			log.Error("status history cleanup failed", "module", "StatusHistory", "error", err.Error())
		}
	}
	go func() {
		recordStatusHistory(statusHistoryContext)
		ticker := time.NewTicker(statushistory.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				recordStatusHistory(statusHistoryContext)
			case <-statusHistoryContext.Done():
				return
			}
		}
	}()
	files := filesystem.New(filesystem.Options{MaxUploadBytes: cfg.Filesystem.MaxUploadBytes})
	ftpService, err := ftpservice.New(db, files, ftpservice.Options{Enabled: cfg.FTP.Enabled, ListenAddr: cfg.FTP.Listen, PublicHost: cfg.FTP.PublicHost, PassivePortStart: cfg.FTP.PassivePortStart, PassivePortEnd: cfg.FTP.PassivePortEnd, TLSCert: cfg.FTP.TLSCert, TLSKey: cfg.FTP.TLSKey, RequireTLS: cfg.FTP.RequireTLS}, logging.WithCategory(log, logging.CategoryFilesystem))
	if err != nil {
		log.Error("FTP service initialization failed", "module", "FTP", "error", err.Error())
		os.Exit(1)
	}
	if err = ftpService.Start(); err != nil {
		log.Error("FTP service start failed", "module", "FTP", "error", err.Error())
		os.Exit(1)
	}
	defer ftpService.Close()
	diagnosticService := diagnostics.New(db, settingService, diagnostics.MonitoringEffective{SampleIntervalSeconds: currentSettings.Monitoring.SampleIntervalSeconds, HistoryLimit: currentSettings.Monitoring.HistoryLimit}, time.Now().UTC())
	catalog := templates.NewCatalogManager(templates.NewOfficialHTTPSource(), cfg.Data.Directory, diagnostics.Version)
	catalog.SetLogger(logging.WithCategory(log, logging.CategoryTemplates))
	templateService := templates.NewServiceWithCatalog(templates.NewStore(db), catalog)
	steamPlatform, err := steamcmd.CurrentPlatform(goRuntime.GOOS)
	if err != nil {
		log.Error("SteamCMD platform unavailable", "error", err.Error())
		os.Exit(1)
	}
	steamManager := steamcmd.New(filepath.Join(cfg.Data.Directory, "tools", "steamcmd"), steamPlatform, nil, nil)
	steamManager.SetLogger(logging.WithCategory(log, logging.CategorySteamCMD))
	provisioner = provisioning.NewWithOptions(db, templateService, steamManager, serverService, cfg.Data.Directory, provisioning.Options{Log: logging.WithCategory(log, logging.CategoryProvisioning), ContainerInstaller: runtime.NewContainerInstaller(dockerEngine), ImagePolicy: provisioning.ImagePolicy{AllowedRegistries: currentSettings.Runtime.ContainerImageAllowlist}})
	// serverupdates reuses the same managed steamManager instance as
	// provisioning: one bootstrap/download implementation, one SteamCMD
	// invocation path, for both initial installation and manual updates.
	serverUpdater := serverupdates.NewWithOptions(db, serverService, steamManager, serverupdates.Options{Log: logging.WithCategory(log, logging.CategoryProvisioning)})
	gameConfigService := gameconfig.New(db, serverService)
	// The server service stays the lifecycle authority; it only asks the
	// configuration service to expand the persisted base launch before start.
	serverService.SetLaunchResolver(gameConfigService)
	defer provisioner.Close()
	defer serverUpdater.Close()
	if err = provisioner.Initialize(context.Background()); err != nil {
		log.Error("provisioning recovery failed", "error", err.Error())
		os.Exit(1)
	}
	log.Info("provisioning recovery completed", "module", "Provisioning.Recovery", "category", logging.CategoryProvisioning)
	if err = serverUpdater.Initialize(context.Background()); err != nil {
		log.Error("server update recovery failed", "error", err.Error())
		os.Exit(1)
	}
	log.Info("server update recovery completed", "module", "ServerUpdates.Recovery", "category", logging.CategoryProvisioning)
	restartScheduleStore := scheduler.NewStore(db)
	restartScheduler := scheduler.New(restartScheduleStore, serverService, scheduler.Options{Audit: audit.New(db), Log: logging.WithCategory(log, logging.CategoryGeneral)})
	if err = restartScheduler.Start(context.Background()); err != nil {
		log.Error("restart scheduler initialization failed", "module", "RestartScheduler", "error", err.Error())
		os.Exit(1)
	}
	defer restartScheduler.Stop()
	apiServer := api.New(auth.New(db), serverService, log, secureCookie, api.Options{TrustLocalProxy: cfg.Server.TrustLocalProxy, Filesystem: files, DataDirectory: cfg.Data.Directory, FTP: ftpService, Settings: settingService, Diagnostics: diagnosticService, Templates: templateService, Provisioning: provisioner, ServerUpdates: serverUpdater, StatusHistory: statusHistory, GameConfig: gameConfigService, Logs: logManager, SetupConfig: configFile, SteamCMD: steamManager, RestartSchedules: restartScheduleStore, RestartScheduler: restartScheduler, EmailAlerts: emailAlerts, EmailVerification: emailVerification})
	// Remote Node Foundation (v0.5A): a bounded, periodic status refresh for
	// this installation's own remote node registry. It never blocks startup
	// and is stopped cleanly on shutdown; see internal/api/node_refresh.go.
	stopHeartbeat := apiServer.StartHeartbeat()
	defer stopHeartbeat()
	handler := apiServer.Handler(static)
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
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
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
