package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"gamenode"
	"gamenode/internal/api"
	"gamenode/internal/auth"
	"gamenode/internal/config"
	"gamenode/internal/console"
	"gamenode/internal/database"
	"gamenode/internal/filesystem"
	"gamenode/internal/runtime"
	"gamenode/internal/servers"
)

//go:embed webassets
var webAssets embed.FS

func main() {
	configPath := flag.String("config", "config.yaml", "Path to YAML configuration")
	flag.Parse()
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(1)
	}
	if err = cfg.EnsureDirectories(); err != nil {
		fmt.Fprintln(os.Stderr, "data directory error:", err)
		os.Exit(1)
	}
	level := new(slog.LevelVar)
	switch strings.ToLower(cfg.Logging.Level) {
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		level.Set(slog.LevelInfo)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
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
	assets, err := fs.Sub(webAssets, "webassets")
	if err != nil {
		log.Error("embedded frontend unavailable", "error", err.Error())
		os.Exit(1)
	}
	static := spaHandler(assets)
	secure := cfg.Server.TLSCert != ""
	serverService := servers.NewService(servers.NewStore(db), runtime.NewNative(), console.NewManager())
	if err = serverService.Rediscover(context.Background()); err != nil {
		log.Error("server rediscovery failed", "error", err.Error())
	}
	files := filesystem.New(filesystem.Options{MaxUploadBytes: cfg.Filesystem.MaxUploadBytes})
	handler := api.New(auth.New(db), serverService, log, secure, api.Options{Filesystem: files}).Handler(static)
	server := &http.Server{Addr: cfg.Server.Listen, Handler: handler, ReadHeaderTimeout: 0, ReadTimeout: 15e9, WriteTimeout: 15e9, IdleTimeout: 60e9}
	log.Info("GameNode starting", "listen", cfg.Server.Listen, "tls", secure)
	if secure {
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
