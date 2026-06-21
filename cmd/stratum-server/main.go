// Command stratum-server is the Stratum control plane binary. It wires the
// platform components (config, logger, telemetry, database, HTTP server) and
// runs until it receives SIGINT or SIGTERM, then shuts down gracefully.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourorg/stratum/internal/api"
	"github.com/yourorg/stratum/internal/api/ws"
	"github.com/yourorg/stratum/internal/iam"
	"github.com/yourorg/stratum/internal/platform/clock"
	"github.com/yourorg/stratum/internal/platform/config"
	"github.com/yourorg/stratum/internal/platform/db"
	"github.com/yourorg/stratum/internal/platform/logger"
	"github.com/yourorg/stratum/internal/platform/telemetry"
	"github.com/yourorg/stratum/internal/run"
	"github.com/yourorg/stratum/internal/secret"
	"github.com/yourorg/stratum/internal/stack"
	"github.com/yourorg/stratum/internal/state"
	"github.com/yourorg/stratum/internal/vcs"
	"github.com/yourorg/stratum/internal/worker"
)

// Version and Commit are injected at build time via -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "error", err)
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.Env)
	log.Info(
		"starting stratum-server",
		"version", Version,
		"commit", Commit,
		"env", cfg.Env,
		"http_port", cfg.HTTPPort,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	telemetryShutdown, err := telemetry.InitTracer(ctx)
	if err != nil {
		log.Error("telemetry init failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetryShutdown(shutdownCtx); err != nil {
			log.Error("telemetry shutdown error", "error", err)
		}
	}()

	database, err := db.New(ctx, cfg.DBURL)
	if err != nil {
		log.Error("database init failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()
	log.Info("database connected")

	crypto, err := secret.NewCrypto(cfg.EncryptionKey)
	if err != nil {
		log.Error("secret crypto init failed", "error", err)
		os.Exit(1)
	}
	iamSvc := iam.NewService(database, cfg.JWTSecret)
	stackSvc := stack.NewService(database)
	secretSvc := secret.NewService(database, crypto)
	stateSvc := state.NewService(database)
	vcsSvc := vcs.NewService(database, cfg.WebhookSecret, log)

	// Phase 2: Run orchestration.
	wsHub := ws.NewHub()
	runSvc := run.NewService(database, wsHub, log)

	sched := run.NewScheduler(
		database,
		run.NewRepository(),
		runSvc,
		stackSvc,
		clock.New(),
		5*time.Second,
		log,
	)
	go sched.Start(ctx)

	// Phase 3: Worker runtime.
	workerSvc := worker.NewService(database, runSvc, stackSvc, secretSvc, cfg.WorkerHMACSecret, log)

	// HTTP server.
	router := api.NewRouter(api.Deps{
		IAMSvc:           iamSvc,
		StackSvc:         stackSvc,
		SecretSvc:        secretSvc,
		StateSvc:         stateSvc,
		VCSSvc:           vcsSvc,
		RunSvc:           runSvc,
		WorkerSvc:        workerSvc,
		WorkerHMACSecret: cfg.WorkerHMACSecret,
		WsHub:            wsHub,
		Logger:           log,
	})
	srv := api.New(":"+cfg.HTTPPort, router, log)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Start()
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-serverErr:
		log.Error("server exited unexpectedly", "error", err)
		os.Exit(1)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("server shutdown error", "error", err)
		os.Exit(1)
	}

	log.Info("stratum-server stopped cleanly")
}
