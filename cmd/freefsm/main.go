package main

import (
	"context"
	"database/sql"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MartialM1nd/freefsm/internal/config"
	"github.com/MartialM1nd/freefsm/internal/database"
	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/handlers"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/justinas/nosurf"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	configFile := flag.String("config", "", "path to config file (optional)")
	seedFlag := flag.Bool("seed", false, "seed demo data and exit")
	flag.Parse()

	if *configFile != "" {
		if err := godotenv.Load(*configFile); err != nil {
			slog.Error("load config file", "error", err)
			os.Exit(1)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
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
			os.Exit(1)
		}
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			slog.Error("open log file", "error", err)
			os.Exit(1)
		}
		defer f.Close()
		logWriter = io.MultiWriter(os.Stdout, f)
	}

	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	slog.Info("starting freefsm", "version", config.Version, "commit", config.Commit)

	if err := os.MkdirAll(cfg.UploadDir, 0750); err != nil {
		slog.Error("create upload directory", "dir", cfg.UploadDir, "error", err)
		os.Exit(1)
	}
	if stat, err := os.Stat(cfg.UploadDir); err != nil || !stat.IsDir() {
		slog.Error("upload directory not accessible", "dir", cfg.UploadDir, "error", err)
		os.Exit(1)
	}
	slog.Info("upload directory ready", "dir", cfg.UploadDir)

	db, err := database.Connect(context.Background(), cfg.DSN())
	if err != nil {
		slog.Error("database connect", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	slog.Info("database connected")

	if err := db.Migrate(context.Background(), database.MigrationFS()); err != nil {
		slog.Error("database migrate", "error", err)
		os.Exit(1)
	}
	slog.Info("database migrations applied")

	if *seedFlag {
		sqldb, err := sql.Open("pgx", cfg.DSN())
		if err != nil {
			slog.Error("ent database connect", "error", err)
			os.Exit(1)
		}
		entClient := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqldb)))
		defer entClient.Close()
		if err := database.Seed(context.Background(), entClient); err != nil {
			slog.Error("seed demo data", "error", err)
			os.Exit(1)
		}
		slog.Info("demo data seeded successfully")
		return
	}

	sessions := services.NewSessionService(db.Pool)

	sqldb, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		slog.Error("ent database connect", "error", err)
		os.Exit(1)
	}
	entClient := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, sqldb)))
	defer entClient.Close()

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(middleware.Flash)
	r.Use(middleware.Theme)
	r.Use(middleware.CSRFToken)
	r.Use(middleware.CurrentPath)
	r.Use(middleware.Company(services.NewCompanySettingsService(entClient)))

	r.Handle("/static/*", http.StripPrefix("/static/", staticHandler()))
	r.Mount("/", handlers.New(db.Pool, entClient, sessions, cfg))

	csrfHandler := nosurf.New(r)
	csrfHandler.SetIsTLSFunc(func(r *http.Request) bool {
		return r.TLS != nil
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		if err := http.ListenAndServe(cfg.Addr, csrfHandler); err != nil {
			slog.Error("server", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
}
