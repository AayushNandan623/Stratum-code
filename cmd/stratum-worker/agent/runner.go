package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

	if result.PlanOutput != nil && (job.RunType == "plan" || job.RunType == "drift_detect") {
		r.reportEvent(ctx, runID, "run.planned", result.PlanOutput)
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
	// For Phase 3, we just return the temp dir. In Phase 4+, we extract the
	// archive to this directory.
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
