// Package policy implements the Stratum policy engine. It defines domain types
// for policies, policy sets, evaluations, and the PolicyService interface.
package policy

import (
	"time"

	"github.com/google/uuid"
)

// ─── Policy ─────────────────────────────────────────────────────────────────

// EnforcementLevel controls whether a policy violation blocks the run.
type EnforcementLevel string

const (
	EnforcementHardFail EnforcementLevel = "HARD_FAIL"
	EnforcementSoftWarn EnforcementLevel = "SOFT_WARN"
)

// PolicySeverity is the overall severity of an evaluation result.
type PolicySeverity string

const (
	SeverityHardFail PolicySeverity = "HARD_FAIL"
	SeveritySoftWarn PolicySeverity = "SOFT_WARN"
	SeverityPass     PolicySeverity = "PASS"
)

// Policy is a single Rego policy rule set.
type Policy struct {
	ID          uuid.UUID        `json:"id"`
	OrgID       uuid.UUID        `json:"org_id"`
	Name        string           `json:"name"`
	Description *string          `json:"description,omitempty"`
	RegoSource  string           `json:"rego_source"`
	Enabled     bool             `json:"enabled"`
	Enforcement EnforcementLevel `json:"enforcement"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	DeletedAt   *time.Time       `json:"deleted_at,omitempty"`
}

// PolicySet is a named group of policies that can be bound to orgs, spaces,
// or stacks.
type PolicySet struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// PolicySetMember joins a policy to a policy set.
type PolicySetMember struct {
	PolicySetID uuid.UUID `json:"policy_set_id"`
	PolicyID    uuid.UUID `json:"policy_id"`
}

// PolicySetBinding attaches a policy set to an org, space, or stack.
type PolicySetBinding struct {
	ID           uuid.UUID `json:"id"`
	PolicySetID  uuid.UUID `json:"policy_set_id"`
	ResourceType string    `json:"resource_type"` // ORG | SPACE | STACK
	ResourceID   uuid.UUID `json:"resource_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// ─── Evaluation types ───────────────────────────────────────────────────────

// ActorContext describes the entity that triggered the run.
type ActorContext struct {
	ID    uuid.UUID `json:"id"`
	Type  string    `json:"type"` // USER | API_KEY | SYSTEM
	Roles []string  `json:"roles"`
}

// StackContext describes the stack being evaluated.
type StackContext struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Space  string            `json:"space"`
}

// ResourceChange describes a single resource change in a plan.
type ResourceChange struct {
	Address string   `json:"address"`
	Type    string   `json:"type"`
	Actions []string `json:"actions"`
	After   any      `json:"after,omitempty"`
	Before  any      `json:"before,omitempty"`
}

// PlanContext describes the plan output for evaluation.
type PlanContext struct {
	ResourceChanges []ResourceChange `json:"resource_changes"`
	TotalAdded      int              `json:"total_added"`
	TotalChanged    int              `json:"total_changed"`
	TotalRemoved    int              `json:"total_removed"`
}

// EvaluationInput is the complete context for a policy evaluation.
type EvaluationInput struct {
	RunID      uuid.UUID     `json:"run_id"`
	OrgID      uuid.UUID     `json:"org_id"`
	StackID    uuid.UUID     `json:"stack_id"`
	SpaceID    *uuid.UUID    `json:"space_id,omitempty"`
	RunType    string        `json:"run_type"`
	Actor      ActorContext  `json:"actor"`
	Stack      StackContext  `json:"stack"`
	PlanOutput *PlanContext  `json:"plan_output,omitempty"`
}

// PolicyViolation describes a single policy rule that was violated.
type PolicyViolation struct {
	PolicyID   uuid.UUID `json:"policy_id"`
	PolicyName string    `json:"policy_name"`
	Message    string    `json:"message"`
	Resource   string    `json:"resource,omitempty"`
}

// EvaluationResult is the outcome of evaluating all applicable policies.
type EvaluationResult struct {
	Allow      bool               `json:"allow"`
	Severity   PolicySeverity     `json:"severity"`
	Violations []PolicyViolation  `json:"violations"`
	PolicyIDs  []uuid.UUID        `json:"policy_ids"`
	DurationMs int64              `json:"duration_ms"`
}

// ─── Input types ────────────────────────────────────────────────────────────

// CreatePolicyInput is the input for creating a new policy.
type CreatePolicyInput struct {
	OrgID       uuid.UUID
	Name        string
	Description *string
	RegoSource  string
	Enabled     *bool
	Enforcement *EnforcementLevel
}

// UpdatePolicyInput is the input for updating a policy's metadata.
type UpdatePolicyInput struct {
	Name        *string
	Description *string
	Enabled     *bool
	Enforcement *EnforcementLevel
}

// CreatePolicySetInput is the input for creating a new policy set.
type CreatePolicySetInput struct {
	OrgID uuid.UUID
	Name  string
}

// DryRunInput is the input for evaluating a policy against hypothetical data.
type DryRunInput struct {
	OrgID    uuid.UUID
	StackID  uuid.UUID
	SpaceID  *uuid.UUID
	PlanJSON string
}
