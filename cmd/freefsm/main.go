package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/MartialM1nd/freefsm/internal/config"
	"github.com/MartialM1nd/freefsm/internal/database"
	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/handlers"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/justinas/nosurf"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
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

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	slog.Info("starting freefsm", "version", config.Version, "commit", config.Commit)

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
	csrfHandler.ExemptFunc(func(r *http.Request) bool {
		return r.URL.Path == "/settings/test-email" ||
			r.URL.Path == "/login" ||
			r.URL.Path == "/forgot-password" ||
			r.URL.Path == "/reset-password" ||
			r.URL.Path == "/setup" ||
			r.URL.Path == "/setup/company"
	})
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
