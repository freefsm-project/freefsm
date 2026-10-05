package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/handlers"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := run(); err != nil {
		slog.Error("freefsm stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configFile := flag.String("config", "", "path to config file (optional)")
	seedFlag := flag.Bool("seed", false, "seed demo data and exit")
	flag.Parse()

	if *configFile != "" {
		if err := godotenv.Load(*configFile); err != nil {
			slog.Error("load config file", "error", err)
			return err
		}
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		return err
	}

	logLevel := new(slog.LevelVar)
	switch cfg.LogLevel {
	case "debug":
		logLevel.Set(slog.LevelDebug)
	case "warn":
		logLevel.Set(slog.LevelWarn)
	case "error":
		logLevel.Set(slog.LevelError)
	default:
		logLevel.Set(slog.LevelInfo)
	}

	var logWriter io.Writer = os.Stdout
	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
			slog.Error("create log directory", "error", err)
			return err
		}
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			slog.Error("open log file", "error", err)
			return err
		}
		defer f.Close()
		logWriter = io.MultiWriter(os.Stdout, f)
	}

	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	slog.Info("starting freefsm", "version", config.Version, "commit", config.Commit)
	control, err := instancecontrol.New(cfg.StateDir)
	if err != nil {
		return err
	}
	var pool *pgxpool.Pool
	disabledReason := config.BackupDisabledReason()
	backupConfig := backup.Config{
		DSN: cfg.DSN(), UploadDir: cfg.UploadDir, StateDir: cfg.StateDir,
		Version: config.Version, Commit: config.Commit, BuildKind: backup.BuildKind(config.BuildKind), Control: control,
		RecoveryOnly: disabledReason != "",
		// Ent uses uncached extended-protocol execution below. Only pgxpool
		// retains prepared statements and needs generation-driven resetting.
		ResetConnections: func() {
			if pool != nil {
				pool.Reset()
			}
		},
	}
	manager, err := backup.New(backupConfig)
	if errors.Is(err, backup.ErrUnsupportedRelease) {
		disabledReason = backup.ErrUnsupportedRelease.Error()
		backupConfig.RecoveryOnly = true
		manager, err = backup.New(backupConfig)
	}
	if err != nil {
		return err
	}
	defer manager.Shutdown()
	if err := manager.Recover(context.Background()); err != nil {
		return err
	}
	// Reserve startup against another process's operation until all migrations,
	// seed work and client construction are finished. No session access can race.
	startupUnlock, err := control.OperationLock()
	if err != nil {
		return err
	}
	defer startupUnlock()
	startupCtx, startupRelease, err := control.Enter(context.Background())
	if err != nil {
		return err
	}
	defer startupRelease()
	if err := manager.RefreshConnections(); err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.UploadDir, 0750); err != nil {
		slog.Error("create upload directory", "dir", cfg.UploadDir, "error", err)
		return err
	}
	if stat, err := os.Stat(cfg.UploadDir); err != nil || !stat.IsDir() {
		slog.Error("upload directory not accessible", "dir", cfg.UploadDir, "error", err)
		return err
	}
	slog.Info("upload directory ready", "dir", cfg.UploadDir)

	db, err := database.Connect(startupCtx, cfg.DSN())
	if err != nil {
		slog.Error("database connect", "error", err)
		return err
	}
	defer db.Close()
	pool = db.Pool
	slog.Info("database connected")

	if err := db.Migrate(startupCtx, database.MigrationFS()); err != nil {
		slog.Error("database migrate", "error", err)
		return err
	}
	slog.Info("database migrations applied")

	if *seedFlag {
		sqldb, err := openEntDB(cfg.DSN())
		if err != nil {
			slog.Error("ent database connect", "error", err)
			return err
		}
		entClient := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqldb)))
		defer entClient.Close()
		if err := database.Seed(startupCtx, entClient); err != nil {
			slog.Error("seed demo data", "error", err)
			return err
		}
		slog.Info("demo data seeded successfully")
		return nil
	}

	sessions := services.NewSessionService(db.Pool)

	sqldb, err := openEntDB(cfg.DSN())
	if err != nil {
		slog.Error("ent database connect", "error", err)
		return err
	}
	entClient := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqldb)))
	defer entClient.Close()
	// Drain engine jobs before closing either application client on every exit.
	defer manager.Shutdown()

	webRouter := chi.NewRouter()
	webRouter.Use(middleware.Flash)
	webRouter.Use(middleware.Theme)
	webRouter.Use(middleware.CSRFToken)
	webRouter.Use(middleware.CurrentPath)
	webRouter.Use(middleware.Company(services.NewCompanySettingsService(entClient)))

	webRouter.Handle("/static/*", http.StripPrefix("/static/", staticHandler()))
	deliveryService := delivery.New(db.Pool, cfg.PublicURL)
	deliveryService.SetInstanceControl(control)
	deliveryService.SetBeforeWork(manager.RefreshConnections)
	webRouter.Get("/delivery/open/{token}", deliveryService.OpenHandler)
	webRouter.Mount("/", handlers.New(db.Pool, entClient, sessions, cfg, control))
	backupRouter := handlers.NewBackupRouter(manager, control, services.NewUserService(entClient), services.NewCompanySettingsService(entClient), sessions, disabledReason)
	applicationHandler := newApplicationHandler(
		apiv1.NewRouter(db.Pool, entClient, sessions),
		webRouter,
		instanceRoutes{control: control, refresh: manager.RefreshConnections, backup: backupRouter},
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	workerEmail := services.NewEmailService(services.NewCompanySettingsService(entClient))
	workerEmail.SetInstanceControl(control)
	worker := &delivery.Worker{
		Service: deliveryService,
		Sender:  delivery.NewSMTPSender(workerEmail),
		Hook:    statusflow.NewAcceptanceHook(statusflow.New(db.Pool)),
	}
	startupRelease()
	startupUnlock()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("document delivery worker stopped", "error", err)
		}
	}()

	srv := newHTTPServer(cfg.Addr, applicationHandler)
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		slog.Error("server", "error", err)
		stop()
		serverErr = nil
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown", "error", err)
		return err
	}
	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
		slog.Warn("document delivery worker drain timed out")
	}
	manager.Shutdown()
	if serverErr == nil {
		return errors.New("HTTP server stopped unexpectedly")
	}
	return nil
}

func openEntDB(dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// Ent's database/sql driver does not explicitly prepare statements. Disable
	// pgx's implicit statement/description caches so schema OIDs cannot survive a
	// restore in idle sql.DB connections. QueryExecModeExec keeps bind parameters.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.StatementCacheCapacity = 0
	cfg.DescriptionCacheCapacity = 0
	return stdlib.OpenDB(*cfg), nil
}
