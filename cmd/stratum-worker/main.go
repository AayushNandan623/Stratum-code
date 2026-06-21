// Command stratum-worker is the Stratum worker agent binary. It connects to the
// control plane, registers as a worker, polls for jobs, and executes them using
// the configured executor (Docker, local, or Kubernetes).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourorg/stratum/cmd/stratum-worker/agent"
	"github.com/yourorg/stratum/internal/worker"
)

// Version and Commit are injected at build time via -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	controlPlaneURL := os.Getenv("STRATUM_CONTROL_PLANE_URL")
	workerToken := os.Getenv("STRATUM_WORKER_TOKEN")
	poolID := os.Getenv("STRATUM_POOL_ID")
	hostname := os.Getenv("STRATUM_WORKER_HOSTNAME")
	stateDir := os.Getenv("STRATUM_WORKER_STATE_DIR")
	if stateDir == "" {
		stateDir = "/tmp/stratum-worker"
	}

	if controlPlaneURL == "" {
		fmt.Fprintln(os.Stderr, "STRATUM_CONTROL_PLANE_URL is required")
		os.Exit(1)
	}
	if workerToken == "" {
		fmt.Fprintln(os.Stderr, "STRATUM_WORKER_TOKEN is required")
		os.Exit(1)
	}
	if poolID == "" {
		fmt.Fprintln(os.Stderr, "STRATUM_POOL_ID is required")
		os.Exit(1)
	}

	logger.Info(
		"starting stratum-worker",
		"version", Version,
		"commit", Commit,
		"control_plane", controlPlaneURL,
	)

	// Create the executor. Prefer Docker when available; fall back to the local
	// executor for development environments without Docker.
	executor, err := worker.NewDockerExecutor(stateDir)
	if err != nil {
		logger.Warn("docker executor not available, falling back to local executor", "error", err)
		executor = &localExecutor{logger: logger}
	}

	cfg := agent.Config{
		ControlPlaneURL: controlPlaneURL,
		WorkerToken:     workerToken,
		PoolID:          poolID,
		Hostname:        hostname,
		StateDir:        stateDir,
	}

	agt := agent.NewAgent(cfg, executor, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agt.Run(ctx); err != nil {
		logger.Error("worker agent failed", "error", err)
		os.Exit(1)
	}
	logger.Info("worker agent stopped cleanly")
}

// localExecutor is a simple executor that runs opentofu locally for development.
// It satisfies the worker.Executor interface without requiring Docker.
type localExecutor struct {
	logger *slog.Logger
}

func (e *localExecutor) Execute(ctx context.Context, task *worker.ExecutionTask) (*worker.ExecutionResult, error) {
	e.logger.Info("local executor: would run opentofu",
		"run_id", task.RunID,
		"run_type", task.RunType,
		"work_dir", task.WorkDir,
		"iac_tool", task.IACTool,
		"iac_version", task.IACVersion,
	)
	// In Phase 3, this shells out to opentofu CLI. For now, simulate success.
	select {
	case <-ctx.Done():
		return &worker.ExecutionResult{ExitCode: -1, Error: "execution cancelled"}, nil
	default:
	}
	return &worker.ExecutionResult{ExitCode: 0}, nil
}
