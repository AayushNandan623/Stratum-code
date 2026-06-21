// Package worker implements the worker runtime bounded context: worker pool and
// worker lifecycle management, job dispatch, and the Docker executor interface.
package worker

import (
	"time"

	"github.com/google/uuid"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// ─── Pool types ─────────────────────────────────────────────────────────────

// PoolType classifies who runs the worker processes.
type PoolType string

const (
	PoolTypeHosted  PoolType = "HOSTED"
	PoolTypePrivate PoolType = "PRIVATE"
)

// WorkerPool is a named group of workers scoped to an organization.
type WorkerPool struct {
	ID             uuid.UUID  `json:"id"`
	OrgID          uuid.UUID  `json:"org_id"`
	Name           string     `json:"name"`
	PoolType       PoolType   `json:"pool_type"`
	TokenHash      string     `json:"-"` // HMAC of pool token, never serialized
	MaxConcurrency int        `json:"max_concurrency"`
	Labels         []byte     `json:"labels"` // JSONB
	CreatedAt      time.Time  `json:"created_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
}

// ─── Worker types ───────────────────────────────────────────────────────────

// WorkerStatus reflects the worker's current liveness state.
type WorkerStatus string

const (
	StatusIDLE         WorkerStatus = "IDLE"
	StatusRUNNING      WorkerStatus = "RUNNING"
	StatusDeregistered WorkerStatus = "DEREGISTERED"
)

// Worker is a registered worker agent instance.
type Worker struct {
	ID             uuid.UUID    `json:"id"`
	PoolID         uuid.UUID    `json:"pool_id"`
	OrgID          uuid.UUID    `json:"org_id"`
	Hostname       string       `json:"hostname"`
	Version        string       `json:"version"`
	Capabilities   []string     `json:"capabilities"`
	Status         WorkerStatus `json:"status"`
	TokenHash      string       `json:"-"` // HMAC of worker token
	LastHeartbeat  *time.Time   `json:"last_heartbeat,omitempty"`
	CurrentRunID   *uuid.UUID   `json:"current_run_id,omitempty"`
	RegisteredAt   time.Time    `json:"registered_at"`
}

// ─── Job types ──────────────────────────────────────────────────────────────

// Job is the domain representation of a run job claimed by a worker.
type Job struct {
	ID         uuid.UUID `json:"id"`
	RunID      uuid.UUID `json:"run_id"`
	PoolID     uuid.UUID `json:"pool_id"`
	RunType    string    `json:"run_type"`
	StackID    uuid.UUID `json:"stack_id"`
	IACTool    string    `json:"iac_tool"`
	IACVersion string    `json:"iac_version"`
	OrgID      uuid.UUID `json:"org_id"`
	Status     string    `json:"status"`    // AVAILABLE | CLAIMED
	Attempt    int       `json:"attempt"`
	CreatedAt  time.Time `json:"created_at"`
}

// ToResponse returns a safe-to-serialize view of the job.
func (j *Job) ToResponse() map[string]any {
	return map[string]any{
		"job_id":      j.ID,
		"run_id":      j.RunID,
		"run_type":    j.RunType,
		"stack_id":    j.StackID,
		"iac_tool":    j.IACTool,
		"iac_version": j.IACVersion,
	}
}

// ─── Input types ────────────────────────────────────────────────────────────

// CreatePoolInput creates a new worker pool.
type CreatePoolInput struct {
	OrgID          uuid.UUID
	Name           string
	PoolType       PoolType
	MaxConcurrency int
	Labels         []byte
}

// RegisterWorkerInput registers a new worker.
type RegisterWorkerInput struct {
	OrgID        uuid.UUID
	PoolID       uuid.UUID
	Hostname     string
	Version      string
	Capabilities []string
	TokenHash    string
}

// HeartbeatInput carries the worker's liveness state.
type HeartbeatInput struct {
	Status       WorkerStatus
	CurrentRunID *uuid.UUID
}

// HeartbeatResponse carries optional cancellation signal from the control plane.
type HeartbeatResponse struct {
	CancelRunID *uuid.UUID `json:"cancel_run_id,omitempty"`
}

// ─── Execution types ────────────────────────────────────────────────────────

// RunType mirrors run.RunType for the worker context.
type WorkerRunType string

const (
	WorkerRunTypePlan        WorkerRunType = "plan"
	WorkerRunTypeApply       WorkerRunType = "apply"
	WorkerRunTypeDestroy     WorkerRunType = "destroy"
	WorkerRunTypeDriftDetect WorkerRunType = "drift_detect"
)

// EnvVar is a key-value environment variable.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// StateBackendConfig tells the executor where to read/write Terraform state.
type StateBackendConfig struct {
	Address       string `json:"address"`
	LockAddress   string `json:"lock_address"`
	UnlockAddress string `json:"unlock_address"`
	LockMethod    string `json:"lock_method"`
	Username      string `json:"username"`
	Password      string `json:"password"`
}

// LogLine mirrors run.LogLine for executor callbacks.
type LogLine struct {
	Line       string    `json:"line"`
	Source     string    `json:"source"` // stdout | stderr
	OccurredAt time.Time `json:"occurred_at"`
}

// ExecutionTask is the full specification the executor needs to run an IaC operation.
type ExecutionTask struct {
	RunID        uuid.UUID         `json:"run_id"`
	StackID      uuid.UUID         `json:"stack_id"`
	OrgID        uuid.UUID         `json:"org_id"`
	RunType      WorkerRunType     `json:"run_type"`
	WorkDir      string            `json:"work_dir"`       // absolute path to extracted IaC source
	IACTool      string            `json:"iac_tool"`       // "opentofu"
	IACVersion   string            `json:"iac_version"`    // "1.6.0"
	Env          []EnvVar          `json:"env"`            // ALL env vars including injected secrets
	StateBackend StateBackendConfig `json:"state_backend"` // where to read/write tfstate
	LogCallback  func(line LogLine) `json:"-"`             // called for each log line as it arrives
	PlanFile     string            `json:"plan_file"`      // path to plan.json (apply from prior plan)
}

// ResourceChange describes a single resource change in a plan.
type ResourceChange struct {
	Address string   `json:"address"`
	Actions []string `json:"actions"` // ["create"] | ["update"] | ["delete"] | ["no-op"]
}

// PlanOutput is the structured result of a plan or drift_detect run.
type PlanOutput struct {
	Raw        []byte           `json:"raw"` // raw plan.json bytes
	HasChanges bool             `json:"has_changes"`
	Added      int              `json:"added"`
	Changed    int              `json:"changed"`
	Removed    int              `json:"removed"`
	Resources  []ResourceChange `json:"resources"`
}

// ExecutionResult is returned by the executor after a run completes.
type ExecutionResult struct {
	ExitCode   int         `json:"exit_code"`
	PlanOutput *PlanOutput `json:"plan_output,omitempty"` // populated on plan/drift_detect
	Error      string      `json:"error,omitempty"`       // empty on success
}

// ─── Sentinel errors ────────────────────────────────────────────────────────

var (
	ErrPoolNotFound   = domainerr.New("POOL_NOT_FOUND", 404, "worker pool not found")
	ErrWorkerNotFound = domainerr.New("WORKER_NOT_FOUND", 404, "worker not found")
	ErrNoJobAvailable = domainerr.New("NO_JOB_AVAILABLE", 204, "no available job")
	ErrInvalidToken   = domainerr.New("INVALID_WORKER_TOKEN", 401, "invalid worker token")
	ErrWorkerRevoked  = domainerr.New("WORKER_TOKEN_REVOKED", 401, "worker token revoked")
	ErrPoolNameExists = domainerr.New("POOL_NAME_EXISTS", 409, "pool name already exists in org")
)
