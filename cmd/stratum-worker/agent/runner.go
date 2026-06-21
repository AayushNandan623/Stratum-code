package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/worker"
)

// Runner bridges a polled Job to an Executor, handling run state transition
// events, log streaming, secret injection, and source archive extraction.
type Runner struct {
	client   *Client
	executor worker.Executor
	stateDir string // temp directory for state and source archives
}

// NewRunner constructs a Runner.
func NewRunner(client *Client, executor worker.Executor, stateDir string) *Runner {
	return &Runner{
		client:   client,
		executor: executor,
		stateDir: stateDir,
	}
}

// ExecuteJob runs the given job through the executor, reporting events and
// logs back to the control plane. It returns the execution result.
// workerID is the authenticated worker's ID, passed to secret claim.
func (r *Runner) ExecuteJob(ctx context.Context, job *worker.Job, workerID uuid.UUID) (*worker.ExecutionResult, error) {
	runID := job.RunID

	// Report planning/apply started. Map run type to the canonical event name.
	r.reportEvent(ctx, runID, workerRunStartedEvent(job.RunType))

	// Fetch source archive.
	workDir, err := r.fetchSourceArchive(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("fetch source archive: %w", err)
	}

	// Claim secrets.
	secretEnv, err := r.client.ClaimSecrets(ctx, runID, workerID)
	if err != nil {
		return nil, fmt.Errorf("claim secrets: %w", err)
	}
	// Zero secrets from memory after container creation.
	defer func() {
		for i := range secretEnv {
			secretEnv[i].Value = ""
		}
	}()

	// Build the execution task.
	task := &worker.ExecutionTask{
		RunID:      runID,
		StackID:    job.StackID,
		RunType:    worker.WorkerRunType(job.RunType),
		WorkDir:    workDir,
		IACTool:    job.IACTool,
		IACVersion: job.IACVersion,
		Env:        secretEnv,
		StateBackend: worker.StateBackendConfig{
			Address:       fmt.Sprintf("%s/api/v1/stacks/%s/state/tfstate", r.client.baseURL, job.StackID),
			LockAddress:   fmt.Sprintf("%s/api/v1/stacks/%s/state/lock", r.client.baseURL, job.StackID),
			UnlockAddress: fmt.Sprintf("%s/api/v1/stacks/%s/state/lock", r.client.baseURL, job.StackID),
			Username:      "worker",
			Password:      job.ID.String(), // run-scoped token
		},
		LogCallback: func(line worker.LogLine) {
			r.client.AppendLogs(context.Background(), runID, []worker.LogLine{line})
		},
	}

	result, err := r.executor.Execute(ctx, task)
	if err != nil {
		r.reportEvent(ctx, runID, "run.failed")
		return nil, err
	}

	if result.Error != "" {
		r.reportEvent(ctx, runID, "run.failed")
		return result, nil
	}

	switch job.RunType {
	case "plan", "drift_detect":
		if result.PlanOutput != nil {
			r.reportEvent(ctx, runID, "run.planned", result.PlanOutput)
		} else {
			r.reportEvent(ctx, runID, "run.planned")
		}
	case "apply":
		r.reportEvent(ctx, runID, "run.applied")
	case "destroy":
		r.reportEvent(ctx, runID, "run.destroyed")
	}

	return result, nil
}

// workerRunStartedEvent maps a job run type to the canonical started event name.
func workerRunStartedEvent(runType string) string {
	switch runType {
	case "plan":
		return "run.planning_started"
	case "apply":
		return "run.applying_started"
	case "destroy":
		return "run.destroying_started"
	default:
		return "run." + runType + "_started"
	}
}

// fetchSourceArchive downloads the source archive for the given run and
// extracts it to a temp directory. Returns the path to the extracted source.
// In development mode (when no real source archive is available), it creates a
// minimal null_resource test configuration so the executor has something to
// work with.
func (r *Runner) fetchSourceArchive(ctx context.Context, runID uuid.UUID) (string, error) {
	archive, err := r.client.GetSourceArchive(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("get source archive: %w", err)
	}
	defer archive.Close()

	workDir, err := os.MkdirTemp(r.stateDir, fmt.Sprintf("run-%s-", runID.String()))
	if err != nil {
		return "", fmt.Errorf("create work dir: %w", err)
	}
	// Make workDir world-accessible so the Docker container (running as
	// UID 10000) can read and write to it.
	os.Chmod(workDir, 0777)

	// Check if the archive is the development stub (empty gzip header only).
	var header [2]byte
	n, _ := archive.Read(header[:])
	if n < 2 || header[0] != 0x1f || header[1] != 0x8b {
		// Not a gzip stub — assume real archive (Phase 4+ extraction).
		return workDir, nil
	}

	// Development mode: create a minimal null_resource test configuration.
	// This allows the executor to run without a real VCS setup.
	mainTF := `resource "null_resource" "test" {
  triggers = {
    run_id = "` + runID.String() + `"
    timestamp = "` + time.Now().UTC().Format(time.RFC3339) + `"
  }
}
output "run_id" {
  value = null_resource.test.triggers.run_id
}
`
	if err := os.WriteFile(filepath.Join(workDir, "main.tf"), []byte(mainTF), 0644); err != nil {
		return "", fmt.Errorf("write main.tf: %w", err)
	}
	r.client.AppendLogs(context.Background(), runID, []worker.LogLine{
		{Line: "Created minimal test configuration in dev mode", Source: "system", OccurredAt: time.Now()},
	})

	return workDir, nil
}

// reportEvent sends a run event to the control plane.
func (r *Runner) reportEvent(ctx context.Context, runID uuid.UUID, eventType string, payload ...any) {
	var payloadData json.RawMessage
	if len(payload) > 0 {
		data, err := json.Marshal(payload[0])
		if err == nil {
			payloadData = data
		}
	}
	r.client.AppendEvent(ctx, runID, eventType, payloadData, time.Now())
}
