// Command ocap-stats collects player statistics from OCAP recordings and serves them over
// an HTTP API, a web page and an admin panel.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/smitt14ua/ocap-statistics/internal/api"
	"github.com/smitt14ua/ocap-statistics/internal/auth"
	"github.com/smitt14ua/ocap-statistics/internal/config"
	"github.com/smitt14ua/ocap-statistics/internal/ocap"
	"github.com/smitt14ua/ocap-statistics/internal/settings"
	"github.com/smitt14ua/ocap-statistics/internal/store"
	"github.com/smitt14ua/ocap-statistics/internal/syncer"
	"github.com/smitt14ua/ocap-statistics/web"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON config file (optional)")
	once := flag.Bool("once", false, "run one sync and exit (no HTTP server)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version, commit, date)
		return
	}
	if err := run(*configPath, *once); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(c config.Log) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(strings.ToUpper(c.Level)))
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(c.Format, "json") {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

func run(configPath string, once bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := newLogger(cfg.Log)
	slog.SetDefault(log)

	st, err := store.Open(cfg.DB)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sm, err := settings.NewManager(ctx, st, settings.FromConfig(cfg), log)
	if err != nil {
		return err
	}
	if err := auth.EnsureAdmin(ctx, st, cfg.Admin, log); err != nil {
		return fmt.Errorf("admin user: %w", err)
	}
	client := ocap.NewClient(cfg.OCAP.URL, cfg.OCAP.DataDir, cfg.OCAP.Token, cfg.OCAP.Timeout.D(), log)
	sy := syncer.New(cfg, client, st, sm, log)

	if once {
		if err := sy.RunTask(ctx, syncer.TaskRecompute); err != nil {
			return err
		}
		return sy.RunTask(ctx, syncer.TaskSync)
	}

	handler, err := api.New(cfg, api.BuildInfo{Version: version, Commit: commit, Date: date}, st, sm, sy, web.FS(), log).Handler()
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "version", version, "ocap", cfg.OCAP.URL, "dataDir", cfg.OCAP.DataDir, "sync", cfg.Sync.Enabled)
		errc <- srv.ListenAndServe()
	}()

	// The task loop always runs (admin tasks); periodic syncing depends on sync.enabled.
	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		sy.Run(ctx, cfg.Sync.Enabled)
	}()

	select {
	case err := <-errc:
		stop()
		<-syncDone
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	// Let the running task stop before the database is closed.
	select {
	case <-syncDone:
	case <-shutdownCtx.Done():
		log.Warn("sync task did not stop in time")
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}
