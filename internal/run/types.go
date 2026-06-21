// Package run implements the Run orchestration bounded context: run creation,
// state machine enforcement, event store, job queue, and the scheduler that
// drives state transitions from PENDING to QUEUED with DAG awareness.
package run

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ─── Run type ───────────────────────────────────────────────────────────────

// RunType classifies the IaC operation the run performs.
type RunType string

const (
	RunTypePlan        RunType = "plan"
	RunTypeApply       RunType = "apply"
	RunTypeDestroy     RunType = "destroy"
	RunTypeDriftDetect RunType = "drift_detect"
)

// TriggerType describes what caused the run to be created.
type TriggerType string

const (
	TriggerManual   TriggerType = "manual"
	TriggerVCSPush  TriggerType = "vcs_push"
	TriggerSchedule TriggerType = "schedule"
	TriggerDrift    TriggerType = "drift"
)

// RunState is the current lifecycle state of a run.
type RunState string

const (
	StatePending          RunState = "PENDING"
	StateQueued           RunState = "QUEUED"
	StateAssigned         RunState = "ASSIGNED"
	StatePlanning         RunState = "PLANNING"
	StatePlanned          RunState = "PLANNED"
	StateAwaitingApproval RunState = "AWAITING_APPROVAL"
	StateApplying         RunState = "APPLYING"
	StateApplied          RunState = "APPLIED"
	StateFailed           RunState = "FAILED"
	StateCancelled        RunState = "CANCELLED"
	StateDiscarded        RunState = "DISCARDED"
	StatePolicyRejected   RunState = "POLICY_REJECTED"
)

// Run is the central domain entity — a single execution of an IaC operation
// against a stack.
type Run struct {
	ID            uuid.UUID  `json:"id"`
	OrgID         uuid.UUID  `json:"org_id"`
	StackID       uuid.UUID  `json:"stack_id"`
	SpaceID       *uuid.UUID `json:"space_id,omitempty"`
	RunType       RunType    `json:"run_type"`
	CurrentState  RunState   `json:"current_state"`
	TriggerType   TriggerType `json:"trigger_type"`
	TriggeredBy   *uuid.UUID `json:"triggered_by,omitempty"`
	ConfigVersion string     `json:"config_version"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ─── Run event ──────────────────────────────────────────────────────────────

// RunEvent records a single state transition or noteworthy occurrence in a
// run's lifecycle.
type RunEvent struct {
	ID         uuid.UUID       `json:"id"`
	RunID      uuid.UUID       `json:"run_id"`
	OrgID      uuid.UUID       `json:"org_id"`
	Seq        int64           `json:"seq"`
	EventType  string          `json:"event_type"`
	ActorID    *uuid.UUID      `json:"actor_id,omitempty"`
	ActorType  string          `json:"actor_type,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
	InsertedAt time.Time       `json:"inserted_at"`
}

// RunEventInput is the user-supplied portion when appending an event.
type RunEventInput struct {
	EventType  string          `json:"event_type"`
	ActorID    *uuid.UUID      `json:"actor_id,omitempty"`
	ActorType  string          `json:"actor_type,omitempty"`
	Payload    json.RawMessage `json:"payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

// ─── Run job ────────────────────────────────────────────────────────────────

// RunJob represents a unit of work claimed by a worker (Phase 3+).
type RunJob struct {
	ID        uuid.UUID  `json:"id"`
	RunID     uuid.UUID  `json:"run_id"`
	PoolID    *uuid.UUID `json:"pool_id,omitempty"`
	Status    string     `json:"status"`
	ClaimedBy *uuid.UUID `json:"claimed_by,omitempty"`
	ClaimedAt *time.Time `json:"claimed_at,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
	Attempt   int        `json:"attempt"`
	CreatedAt time.Time  `json:"created_at"`
}

// ─── Log line ───────────────────────────────────────────────────────────────

// LogLine is a single line of stdout or stderr output from a run.
type LogLine struct {
	Line       string    `json:"line"`
	Source     string    `json:"source"` // stdout | stderr
	OccurredAt time.Time `json:"occurred_at"`
}

// ─── Input types ────────────────────────────────────────────────────────────

// CreateRunInput holds the fields for creating a new run.
type CreateRunInput struct {
	OrgID         uuid.UUID
	StackID       uuid.UUID
	SpaceID       *uuid.UUID
	RunType       RunType
	TriggerType   TriggerType
	TriggeredBy   *uuid.UUID
	ConfigVersion string
}

// RunFilter specifies the filter criteria for listing runs.
type RunFilter struct {
	OrgID       uuid.UUID
	StackID     *uuid.UUID
	States      []RunState
	TriggerType *TriggerType
	Page        int
	Size        int
}

// Pagination controls list endpoints in the run context.
type Pagination struct {
	Page int
	Size int
}

// ─── Plan output ────────────────────────────────────────────────────────────

// ResourceChange describes a single resource change in a plan.
type ResourceChange struct {
	Address string   `json:"address"`
	Actions []string `json:"actions"` // ["create"] | ["update"] | ["delete"] | ["no-op"]
}

// PlanOutput is the structured result of a plan or drift_detect run.
type PlanOutput struct {
	Raw        []byte           `json:"raw"`
	HasChanges bool             `json:"has_changes"`
	Added      int              `json:"added"`
	Changed    int              `json:"changed"`
	Removed    int              `json:"removed"`
	Resources  []ResourceChange `json:"resources"`
}
