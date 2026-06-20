// Package stack implements the Stack management bounded context: CRUD,
// variable management, and the dependency graph with cycle detection. It never
// imports the iam context; tenancy is enforced by passing the caller's orgID
// (extracted from the request identity by the handler) into service methods.
package stack

import (
	"context"
	"time"

	"github.com/google/uuid"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// StackStatus is the lifecycle state of a stack.
type StackStatus string

const (
	StatusActive    StackStatus = "ACTIVE"
	StatusDrifted   StackStatus = "DRIFTED"
	StatusLocked    StackStatus = "LOCKED"
	StatusDestroyed StackStatus = "DESTROYED"
)

// DriftMode controls how detected drift is handled.
type DriftMode string

const (
	DriftNotify    DriftMode = "NOTIFY"
	DriftAutoApply DriftMode = "AUTO_APPLY"
	DriftOff       DriftMode = "OFF"
)

// Stack is an infrastructure stack managed by the control plane.
type Stack struct {
	ID                uuid.UUID      `json:"id"`
	OrgID             uuid.UUID      `json:"org_id"`
	SpaceID           *uuid.UUID     `json:"space_id,omitempty"`
	Name              string         `json:"name"`
	Status            StackStatus    `json:"status"`
	VCSRepo           string         `json:"vcs_repo"`
	VCSBranch         string         `json:"vcs_branch"`
	WorkingDir        string         `json:"working_dir"`
	IACTool           string         `json:"iac_tool"`
	IACVersion        string         `json:"iac_version"`
	WorkerPoolID      *uuid.UUID     `json:"worker_pool_id,omitempty"`
	AutoApply         bool           `json:"auto_apply"`
	ReconcileInterval time.Duration  `json:"reconcile_interval_ns"`
	DriftMode         DriftMode      `json:"drift_mode"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         *time.Time     `json:"deleted_at,omitempty"`
}

// Variable is a typed input to a stack (terraform or env).
type Variable struct {
	ID        uuid.UUID `json:"id"`
	StackID   uuid.UUID `json:"stack_id"`
	Key       string    `json:"key"`
	Value     string    `json:"value,omitempty"`
	Sensitive bool      `json:"sensitive"`
	Category  string    `json:"category"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Dependency is a directed edge: StackID depends on DependsOnID.
type Dependency struct {
	StackID     uuid.UUID `json:"stack_id"`
	DependsOnID uuid.UUID `json:"depends_on_id"`
}

// DependencyEdge is a proposed edge used for cycle checking.
type DependencyEdge struct {
	StackID     uuid.UUID
	DependsOnID uuid.UUID
}

// UpstreamStatus reports the state of a stack's dependency.
type UpstreamStatus struct {
	StackID uuid.UUID
	Name    string
	Status  StackStatus
}

// Pagination controls list endpoints.
type Pagination struct {
	Page int
	Size int
}

// CreateStackInput holds the fields for creating a stack.
type CreateStackInput struct {
	OrgID             uuid.UUID
	SpaceID           *uuid.UUID
	Name              string
	VCSRepo           string
	VCSBranch         string
	WorkingDir        string
	IACTool           string
	IACVersion        string
	WorkerPoolID      *uuid.UUID
	AutoApply         bool
	ReconcileInterval time.Duration
	DriftMode         DriftMode
}

// UpdateStackInput holds optional fields for patching a stack. Nil fields are
// left unchanged.
type UpdateStackInput struct {
	VCSRepo           *string
	VCSBranch         *string
	WorkingDir        *string
	IACTool           *string
	IACVersion        *string
	AutoApply         *bool
	ReconcileInterval *time.Duration
	DriftMode         *DriftMode
	Status            *StackStatus
}

// VariableInput holds the fields for setting a variable.
type VariableInput struct {
	Key       string
	Value     string
	Sensitive bool
	Category  string
}

// StackService is the boundary contract for the stack context. Methods that
// operate on a single resource take orgID for tenancy scoping.
type StackService interface {
	Create(ctx context.Context, input CreateStackInput) (*Stack, error)
	Get(ctx context.Context, orgID, id uuid.UUID) (*Stack, error)
	GetByOrgID(ctx context.Context, orgID uuid.UUID, page Pagination) ([]*Stack, int, error)
	Update(ctx context.Context, orgID, id uuid.UUID, input UpdateStackInput) (*Stack, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
	SetStatus(ctx context.Context, orgID, id uuid.UUID, status StackStatus) error

	// Variables
	SetVariable(ctx context.Context, orgID, stackID uuid.UUID, input VariableInput) error
	DeleteVariable(ctx context.Context, orgID, stackID uuid.UUID, key string) error
	ListVariables(ctx context.Context, orgID, stackID uuid.UUID) ([]*Variable, error)

	// Dependency graph
	AddDependency(ctx context.Context, orgID, stackID, dependsOnID uuid.UUID) error
	RemoveDependency(ctx context.Context, orgID, stackID, dependsOnID uuid.UUID) error
	GetDependencies(ctx context.Context, orgID, stackID uuid.UUID) ([]*Dependency, error)
	GetDependents(ctx context.Context, orgID, stackID uuid.UUID) ([]*Dependency, error)
	IsDAGCyclePresent(ctx context.Context, orgID uuid.UUID, proposedEdge DependencyEdge) (bool, error)

	// Used by scheduler for DAG-aware dispatch (stubs until Phase 2)
	HasActiveRun(ctx context.Context, stackID uuid.UUID) (bool, error)
	GetUpstreamStatus(ctx context.Context, stackID uuid.UUID) ([]UpstreamStatus, error)

	// ListByVCS returns stacks matching a repo URL and branch. Used by the
	// webhook receiver; it is a system-level lookup without org scoping.
	ListByVCS(ctx context.Context, repo, branch string) ([]*Stack, error)
}

// Sentinel domain errors.
var (
	ErrStackNotFound     = domainerr.New("STACK_NOT_FOUND", 404, "stack not found")
	ErrDuplicateStack    = domainerr.New("STACK_DUPLICATE", 409, "stack with this name already exists in org")
	ErrDependencyCycle   = domainerr.New("DEPENDENCY_CYCLE", 409, "adding this dependency would create a cycle")
	ErrDependencyExists  = domainerr.New("DEPENDENCY_EXISTS", 409, "dependency already exists")
	ErrDependencyNotFound = domainerr.New("DEPENDENCY_NOT_FOUND", 404, "dependency not found")
	ErrVariableNotFound  = domainerr.New("VARIABLE_NOT_FOUND", 404, "variable not found")
	ErrSelfDependency    = domainerr.New("SELF_DEPENDENCY", 422, "a stack cannot depend on itself")
)
