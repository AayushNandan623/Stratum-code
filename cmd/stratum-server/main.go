// Command stratum-server is the Stratum control plane binary. It wires the
// platform components (config, logger, telemetry, database, NATS, HTTP server)
// and runs until it receives SIGINT or SIGTERM, then shuts down gracefully.
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
	"github.com/yourorg/stratum/internal/events"
	"github.com/yourorg/stratum/internal/events/consumers"
	"github.com/yourorg/stratum/internal/iam"
	"github.com/yourorg/stratum/internal/platform/clock"
	"github.com/yourorg/stratum/internal/platform/config"
	"github.com/yourorg/stratum/internal/platform/db"
	"github.com/yourorg/stratum/internal/platform/logger"
	"github.com/yourorg/stratum/internal/platform/telemetry"
	"github.com/yourorg/stratum/internal/policy"
	"github.com/yourorg/stratum/internal/reconcile"
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

	// Phase 6: NATS connection.
	natsBus, err := events.NewNATSBus(ctx, cfg.NATSUrl, log)
	if err != nil {
		log.Error("nats init failed", "error", err)
		os.Exit(1)
	}
	js := natsBus.JetStream()
	log.Info("nats connected", "url", cfg.NATSUrl)

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

	// Phase 4: Policy engine.
	policyRepo := policy.NewRepository()
	bundleLoader := policy.NewBundleLoader(policyRepo, database.Pool, log)
	go bundleLoader.Start(ctx)
	policySvc := policy.NewService(database, bundleLoader, log)

	// Phase 2: Run orchestration. Pass nil for hub (NATS handles distribution).
	wsHub := ws.NewNATSHub(js)
	wsHub.WithLogger(log)
	runSvc := run.NewService(database, wsHub, log)

	// Phase 5: Reconciler (needs runSvc; then sets itself as run's drift handler).
	reconcileSvc := reconcile.NewService(database, runSvc, stackSvc, log)
	reconcileCtrl := reconcile.NewController(database, runSvc, stackSvc, 5, log)
	go reconcileCtrl.Start(ctx)
	runSvc.SetDriftHandler(reconcileSvc)

	sched := run.NewScheduler(
		database,
		run.NewRepository(),
		runSvc,
		stackSvc,
		policySvc,
		clock.New(),
		5*time.Second,
		log,
	)
	go sched.Start(ctx)

	// Phase 3: Worker runtime.
	workerSvc := worker.NewService(database, runSvc, stackSvc, secretSvc, cfg.WorkerHMACSecret, log)

	// Phase 6: Start outbox relay.
	outboxRelay := events.NewOutboxRelay(database.Pool, js, log).
		WithTick(time.Duration(cfg.OutboxTickMs) * time.Millisecond).
		WithBatch(cfg.OutboxBatchSize)
	go outboxRelay.Start(ctx)

	// Phase 6: Start NATS consumers.
	auditArchiver := consumers.NewAuditArchiver(database.Pool, js, log)
	go func() {
		if err := auditArchiver.Start(ctx); err != nil {
			log.Error("audit archiver exited", "error", err)
		}
	}()

	notifyRouter := consumers.NewNotificationRouter(js, cfg.SlackWebhookURL, log)
	go func() {
		if err := notifyRouter.Start(ctx); err != nil {
			log.Error("notification router exited", "error", err)
		}
	}()

	reconcileTrigger := consumers.NewReconcileTriggerConsumer(reconcileSvc, js, log)
	go func() {
		if err := reconcileTrigger.Start(ctx); err != nil {
			log.Error("reconcile trigger consumer exited", "error", err)
		}
	}()

	// HTTP server — pass NATS hub.
	router := api.NewRouter(api.Deps{
		IAMSvc:           iamSvc,
		StackSvc:         stackSvc,
		SecretSvc:        secretSvc,
		PolicySvc:        policySvc,
		ReconcileSvc:     reconcileSvc,
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
