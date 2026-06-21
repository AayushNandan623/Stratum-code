package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/worker"
)

// Version is the worker agent version, set at build time.
var Version = "dev"

// Agent manages the worker lifecycle: registration, heartbeat, job polling,
// and graceful shutdown.
type Agent struct {
	config          Config
	client          *Client
	executor        worker.Executor
	runner          *Runner
	workerID        uuid.UUID
	heartbeatSec    int
	currentStatus   worker.WorkerStatus
	currentRunID    *uuid.UUID
	cancelCurrentRun context.CancelFunc
	mu              sync.Mutex
	stopCh          chan struct{}
	logger          *slog.Logger
}

// Config holds the worker agent configuration.
type Config struct {
	ControlPlaneURL string // STRATUM_CONTROL_PLANE_URL
	WorkerToken     string // STRATUM_WORKER_TOKEN
	PoolID          string // worker pool ID
	Hostname        string
	StateDir        string // directory for state files and source archives
}

// NewAgent creates a new Agent with the given config, executor, and logger.
func NewAgent(cfg Config, executor worker.Executor, logger *slog.Logger) *Agent {
	client := NewClient(cfg.ControlPlaneURL)
	client.SetToken(cfg.WorkerToken)

	stateDir := cfg.StateDir
	if stateDir == "" {
		stateDir = "/tmp/stratum-worker"
	}
	os.MkdirAll(stateDir, 0755)

	return &Agent{
		config:        cfg,
		client:        client,
		executor:      executor,
		runner:        NewRunner(client, executor, stateDir),
		currentStatus: worker.StatusIDLE,
		stopCh:        make(chan struct{}),
		logger:        logger,
	}
}

// Run starts the agent loop. It blocks until ctx is cancelled or a fatal error
// occurs. On return, the worker is deregistered from the control plane.
func (a *Agent) Run(ctx context.Context) error {
	hostname := a.config.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}

	// Register with the control plane.
	workerID, heartbeatSec, err := a.client.Register(ctx,
		a.config.PoolID, hostname, Version, []string{"opentofu"})
	if err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	a.workerID = workerID
	a.heartbeatSec = heartbeatSec
	a.logger.Info("worker registered", "worker_id", workerID, "heartbeat_interval", heartbeatSec)

	// Start the heartbeat goroutine.
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	go a.heartbeatLoop(heartbeatCtx)

	// Deregister on exit.
	defer func() {
		a.logger.Info("deregistering worker")
		a.client.Deregister(context.Background(), a.workerID)
	}()

	a.logger.Info("worker agent started, entering job loop")

	// Job polling loop.
	for {
		select {
		case <-ctx.Done():
			a.logger.Info("worker agent stopping")
			return nil
		default:
		}

		job, err := a.client.PollJob(ctx, workerID, 30*time.Second)
		if err == ErrNoJob {
			continue
		}
		if err != nil {
			a.logger.Error("job poll error", "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		a.logger.Info("job claimed", "run_id", job.RunID, "run_type", job.RunType)

		// Set current run status for heartbeat.
		a.setCurrentRun(job.RunID, worker.StatusRUNNING)
		jobCtx, cancelJob := context.WithCancel(ctx)
		a.cancelCurrentRun = cancelJob

		// Execute the job.
		result, err := a.runner.ExecuteJob(jobCtx, job, a.workerID)

		// Clear current run.
		a.setCurrentRun(uuid.Nil, worker.StatusIDLE)
		a.cancelCurrentRun = nil

		if err != nil {
			a.logger.Error("job execution failed", "run_id", job.RunID, "error", err)
			continue
		}

		a.logger.Info("job completed", "run_id", job.RunID, "exit_code", result.ExitCode)
	}
}

// heartbeatLoop sends periodic heartbeats to the control plane.
func (a *Agent) heartbeatLoop(ctx context.Context) {
	interval := time.Duration(a.heartbeatSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.mu.Lock()
			status := a.currentStatus
			runID := a.currentRunID
			a.mu.Unlock()

			resp, err := a.client.Heartbeat(ctx, a.workerID, string(status), runID)
			if err != nil {
				a.logger.Warn("heartbeat failed", "error", err)
				continue
			}
			if resp != nil && resp.CancelRunID != nil {
				a.logger.Info("received cancellation signal", "run_id", *resp.CancelRunID)
				a.mu.Lock()
				if a.cancelCurrentRun != nil {
					a.cancelCurrentRun()
				}
				a.mu.Unlock()
			}
		case <-ctx.Done():
			return
		}
	}
}

// setCurrentRun updates the current run state for heartbeat reporting.
func (a *Agent) setCurrentRun(runID uuid.UUID, status worker.WorkerStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.currentStatus = status
	if runID == uuid.Nil {
		a.currentRunID = nil
	} else {
		a.currentRunID = &runID
	}
}
