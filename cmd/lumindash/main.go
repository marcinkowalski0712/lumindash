package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"lumindash/internal/config"
	"lumindash/internal/db"
	"lumindash/internal/handlers"
)

const lumindashVersion = "0.1.0"

func main() {
	ctx := context.Background()

	// ── Structured logging ────────────────────────────────────────────────────
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// ── Config ────────────────────────────────────────────────────────────────
	cfg := config.Load()

	// ── Database ──────────────────────────────────────────────────────────────
	database, err := db.New(ctx, cfg.DSN())
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	slog.Info("database connected", "host", cfg.DBHost, "name", cfg.DBName)

	// ── Zabbix version detection ──────────────────────────────────────────────
	rawVersion, err := database.DetectVersion(ctx)
	if err != nil {
		slog.Error("failed to detect Zabbix version from database", "error", err)
		os.Exit(1)
	}

	zbxVer := config.ParseRawVersion(rawVersion)
	if !zbxVer.IsSupported() {
		slog.Error("unsupported Zabbix version",
			"raw", rawVersion,
			"version", zbxVer.String(),
			"minimum", fmt.Sprintf("%d (6.0.0)", config.MinSupportedRaw),
		)
		os.Exit(1)
	}

	slog.Info("detected Zabbix version", "version", zbxVer.String(), "raw", zbxVer.Raw)
	if zbxVer.IsAlpha {
		slog.Warn("detected Zabbix alpha release — experimental support, some views may be incomplete",
			"version", zbxVer.String())
	}
	if zbxVer.IsEOL() {
		slog.Warn("detected Zabbix EOL version",
			"version", zbxVer.String(),
			"eol", zbxVer.EOLDate().Format("2006-01-02"),
		)
	}

	// ── Optional features: TimescaleDB / partitioned history ─────────────────
	hasTSD, err := database.HasTimescaleDB(ctx)
	if err != nil {
		slog.Warn("could not detect TimescaleDB", "error", err)
	} else if hasTSD {
		slog.Info("TimescaleDB detected — using compression-aware queries")
	}

	hasPart, err := database.HasPartitionedHistory(ctx)
	if err != nil {
		slog.Warn("could not detect partitioned history", "error", err)
	} else if hasPart {
		slog.Info("native partitioned history detected")
	}

	// ── Schema manifest (Adapter80 only) ─────────────────────────────────────
	var manifest *db.SchemaManifest
	if zbxVer.Major >= 8 {
		manifest, err = database.InspectSchema(ctx)
		if err != nil {
			slog.Error("Adapter80: schema inspection failed — proceeding with best-effort", "error", err)
		} else {
			slog.Info("Adapter80: schema manifest cached",
				"tables", len(manifest.Tables))
		}
	}

	// ── Adapter selection ─────────────────────────────────────────────────────
	adapter, err := db.NewAdapter(rawVersion, database, manifest)
	if err != nil {
		slog.Error("failed to create query adapter", "error", err)
		os.Exit(1)
	}
	slog.Info("query adapter selected", "adapter", zbxVer.AdapterName())

	// ── Health state ──────────────────────────────────────────────────────────
	healthState := &handlers.HealthState{
		ZabbixVersion:        zbxVer,
		TimescaleDB:          hasTSD,
		PartitionedHistory:   hasPart,
		SchemaManifestCached: manifest != nil,
		Adapter:              zbxVer.AdapterName(),
		LumindashVersion:     lumindashVersion,
	}

	// ── Router ────────────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))

	// Health check
	r.Get("/healthz", handlers.HealthHandler(database, healthState))

	// Version banner middleware — injects zbxVer into request context
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), handlers.CtxKeyZabbixVersion, zbxVer)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})

	// Page routes (registered by page handlers — stubs until implemented)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})
	r.Get("/dashboard", handlers.DashboardHandler(adapter, zbxVer))
	r.Get("/hosts", handlers.HostsHandler(adapter, database))
	r.Get("/hosts/{hostID}", handlers.HostDetailHandler(adapter))
	r.Get("/metrics", handlers.MetricsHandler(adapter, database))
	r.Get("/events", handlers.EventsHandler(adapter, database))
	r.Get("/config", handlers.ConfigHandler(adapter))

	// API endpoints (write → Zabbix JSON-RPC; read → DB)
	r.Post("/api/events/{eventID}/acknowledge", handlers.AcknowledgeHandler(cfg))
	r.Get("/api/hosts/{hostID}/items", handlers.ItemsAPIHandler(adapter))
	r.Get("/api/metrics", handlers.MetricsAPIHandler(adapter))
	r.Post("/api/hosts/{hostID}/status", handlers.HostStatusHandler(cfg))
	r.Post("/api/triggers/{triggerID}/status", handlers.TriggerStatusHandler(cfg))

	// Static assets
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))))

	// ── HTTP server ───────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  60 * time.Second,
	}

	slog.Info("lumindash starting",
		"addr", cfg.ListenAddr,
		"zabbix", zbxVer.String(),
		"adapter", zbxVer.AdapterName(),
		"version", lumindashVersion,
	)

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("shutting down…")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	}
	slog.Info("lumindash stopped")
}
