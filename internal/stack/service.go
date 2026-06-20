// Stack service: orchestrates stack CRUD, variables, and the dependency graph.
// Cycle detection is enforced here before any edge is persisted.
package stack

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

type service struct {
	repo *Repository
	db   *db.DB
}

// NewService constructs a StackService backed by database.
func NewService(database *db.DB) StackService {
	return &service{repo: NewRepository(), db: database}
}

var _ StackService = (*service)(nil)

// Create inserts a new stack after applying field defaults.
func (s *service) Create(ctx context.Context, in CreateStackInput) (*Stack, error) {
	if in.OrgID == uuid.Nil || in.Name == "" {
		return nil, domainerr.ErrValidation
	}
	if in.IACTool == "" {
		in.IACTool = "opentofu"
	}
	if in.IACVersion == "" {
		in.IACVersion = "latest"
	}
	if in.VCSBranch == "" {
		in.VCSBranch = "main"
	}
	if in.WorkingDir == "" {
		in.WorkingDir = "."
	}
	if in.DriftMode == "" {
		in.DriftMode = DriftNotify
	}
	if in.ReconcileInterval <= 0 {
		in.ReconcileInterval = time.Hour
	}
	return s.repo.Create(ctx, s.db.Pool, in)
}

func (s *service) Get(ctx context.Context, orgID, id uuid.UUID) (*Stack, error) {
	return s.repo.GetByID(ctx, s.db.Pool, orgID, id)
}

func (s *service) GetByOrgID(ctx context.Context, orgID uuid.UUID, page Pagination) ([]*Stack, int, error) {
	return s.repo.ListByOrg(ctx, s.db.Pool, orgID, page)
}

func (s *service) Update(ctx context.Context, orgID, id uuid.UUID, in UpdateStackInput) (*Stack, error) {
	return s.repo.Update(ctx, s.db.Pool, orgID, id, in)
}

func (s *service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.repo.SoftDelete(ctx, s.db.Pool, orgID, id)
}

func (s *service) SetStatus(ctx context.Context, orgID, id uuid.UUID, status StackStatus) error {
	return s.repo.SetStatus(ctx, s.db.Pool, orgID, id, status)
}

// ensureStack verifies a stack exists in the org, returning ErrStackNotFound
// otherwise. Used to scope variable and dependency operations.
func (s *service) ensureStack(ctx context.Context, orgID, stackID uuid.UUID) error {
	_, err := s.repo.GetByID(ctx, s.db.Pool, orgID, stackID)
	return err
}

func (s *service) SetVariable(ctx context.Context, orgID, stackID uuid.UUID, in VariableInput) error {
	if in.Key == "" {
		return domainerr.ErrValidation
	}
	if err := s.ensureStack(ctx, orgID, stackID); err != nil {
		return err
	}
	return s.repo.SetVariable(ctx, s.db.Pool, stackID, in)
}

func (s *service) DeleteVariable(ctx context.Context, orgID, stackID uuid.UUID, key string) error {
	if err := s.ensureStack(ctx, orgID, stackID); err != nil {
		return err
	}
	return s.repo.DeleteVariable(ctx, s.db.Pool, stackID, key)
}

func (s *service) ListVariables(ctx context.Context, orgID, stackID uuid.UUID) ([]*Variable, error) {
	if err := s.ensureStack(ctx, orgID, stackID); err != nil {
		return nil, err
	}
	return s.repo.ListVariables(ctx, s.db.Pool, stackID)
}

// AddDependency validates both stacks, checks for a cycle, then persists the
// edge. A cycle returns ErrDependencyCycle (409).
func (s *service) AddDependency(ctx context.Context, orgID, stackID, dependsOnID uuid.UUID) error {
	if stackID == dependsOnID {
		return ErrSelfDependency
	}
	if err := s.ensureStack(ctx, orgID, stackID); err != nil {
		return err
	}
	if err := s.ensureStack(ctx, orgID, dependsOnID); err != nil {
		return err
	}
	cyclic, err := s.IsDAGCyclePresent(ctx, orgID, DependencyEdge{StackID: stackID, DependsOnID: dependsOnID})
	if err != nil {
		return err
	}
	if cyclic {
		return ErrDependencyCycle
	}
	return s.repo.AddDependency(ctx, s.db.Pool, stackID, dependsOnID)
}

func (s *service) RemoveDependency(ctx context.Context, orgID, stackID, dependsOnID uuid.UUID) error {
	if err := s.ensureStack(ctx, orgID, stackID); err != nil {
		return err
	}
	return s.repo.RemoveDependency(ctx, s.db.Pool, stackID, dependsOnID)
}

func (s *service) GetDependencies(ctx context.Context, orgID, stackID uuid.UUID) ([]*Dependency, error) {
	if err := s.ensureStack(ctx, orgID, stackID); err != nil {
		return nil, err
	}
	return s.repo.ListDependencies(ctx, s.db.Pool, stackID)
}

func (s *service) GetDependents(ctx context.Context, orgID, stackID uuid.UUID) ([]*Dependency, error) {
	if err := s.ensureStack(ctx, orgID, stackID); err != nil {
		return nil, err
	}
	return s.repo.ListDependents(ctx, s.db.Pool, stackID)
}

// IsDAGCyclePresent loads the org-wide dependency graph, adds the proposed
// edge, and runs DFS coloring from the edge's source.
func (s *service) IsDAGCyclePresent(ctx context.Context, orgID uuid.UUID, edge DependencyEdge) (bool, error) {
	deps, err := s.repo.ListAllDependenciesByOrg(ctx, s.db.Pool, orgID)
	if err != nil {
		return false, err
	}
	adj := buildAdjacencyWithEdge(deps, edge)
	return hasCycle(adj, edge.StackID), nil
}

// HasActiveRun is a stub until the run context lands in Phase 2.
func (s *service) HasActiveRun(_ context.Context, _ uuid.UUID) (bool, error) {
	return false, nil
}

// GetUpstreamStatus is a stub until the run context lands in Phase 2.
func (s *service) GetUpstreamStatus(_ context.Context, _ uuid.UUID) ([]UpstreamStatus, error) {
	return nil, nil
}

// ListByVCS returns stacks matching a repo URL and branch (system-level lookup
// for the webhook receiver).
func (s *service) ListByVCS(ctx context.Context, repo, branch string) ([]*Stack, error) {
	return s.repo.ListByVCS(ctx, s.db.Pool, repo, branch)
}
