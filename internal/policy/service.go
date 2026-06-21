package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/open-policy-agent/opa/v1/ast"

	"github.com/yourorg/stratum/internal/platform/db"
	domainerr "github.com/yourorg/stratum/internal/platform/errors"
)

// PolicyService is the boundary contract for the policy context.
type PolicyService interface {
	// Policy management
	Create(ctx context.Context, input CreatePolicyInput) (*Policy, error)
	Get(ctx context.Context, id uuid.UUID) (*Policy, error)
	List(ctx context.Context, orgID uuid.UUID) ([]*Policy, error)
	Update(ctx context.Context, id uuid.UUID, input UpdatePolicyInput) (*Policy, error)
	UpdateSource(ctx context.Context, id uuid.UUID, source string) error
	Delete(ctx context.Context, id uuid.UUID) error

	// Policy sets
	CreatePolicySet(ctx context.Context, input CreatePolicySetInput) (*PolicySet, error)
	AddToSet(ctx context.Context, setID, policyID uuid.UUID) error
	RemoveFromSet(ctx context.Context, setID, policyID uuid.UUID) error
	BindSet(ctx context.Context, setID uuid.UUID, resourceType string, resourceID uuid.UUID) (*PolicySetBinding, error)
	UnbindSet(ctx context.Context, bindingID uuid.UUID) error

	// Evaluation — called by run scheduler
	Evaluate(ctx context.Context, input EvaluationInput) (*EvaluationResult, error)

	// Dry-run evaluation for UI/API consumers
	DryRun(ctx context.Context, input DryRunInput) (*EvaluationResult, error)
}

type service struct {
	repo     *Repository
	eval     *OPAEvaluator
	loader   *BundleLoader
	db       *db.DB
	logger   *slog.Logger
}

var _ PolicyService = (*service)(nil)

// NewService constructs a PolicyService.
func NewService(database *db.DB, loader *BundleLoader, logger *slog.Logger) PolicyService {
	eval := NewOPAEvaluator(loader)
	return &service{
		repo:   NewRepository(),
		eval:   eval,
		loader: loader,
		db:     database,
		logger: logger,
	}
}

// ─── Policy CRUD ────────────────────────────────────────────────────────────

func (s *service) Create(ctx context.Context, input CreatePolicyInput) (*Policy, error) {
	if input.OrgID == uuid.Nil || input.Name == "" || input.RegoSource == "" {
		return nil, domainerr.ErrValidation
	}
	if err := validateRego(input.RegoSource, input.Name); err != nil {
		return nil, domainerr.New("INVALID_REGO", 422, err.Error())
	}
	if input.Enabled == nil {
		input.Enabled = boolPtr(true)
	}
	if input.Enforcement == nil {
		input.Enforcement = enforcementPtr(EnforcementSoftWarn)
	}
	now := time.Now()
	p := &Policy{
		ID:          uuid.New(),
		OrgID:       input.OrgID,
		Name:        input.Name,
		Description: input.Description,
		RegoSource:  input.RegoSource,
		Enabled:     *input.Enabled,
		Enforcement: *input.Enforcement,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.Create(ctx, s.db.Pool, p); err != nil {
		return nil, err
	}
	s.loader.NotifyUpdate(p.ID)
	return p, nil
}

func (s *service) Get(ctx context.Context, id uuid.UUID) (*Policy, error) {
	return s.repo.GetByID(ctx, s.db.Pool, id)
}

func (s *service) List(ctx context.Context, orgID uuid.UUID) ([]*Policy, error) {
	return s.repo.ListByOrg(ctx, s.db.Pool, orgID)
}

func (s *service) Update(ctx context.Context, id uuid.UUID, input UpdatePolicyInput) (*Policy, error) {
	p, err := s.repo.Update(ctx, s.db.Pool, id, input)
	if err != nil {
		return nil, err
	}
	s.loader.NotifyUpdate(id)
	return p, nil
}

func (s *service) UpdateSource(ctx context.Context, id uuid.UUID, source string) error {
	if err := validateRego(source, "update"); err != nil {
		return domainerr.New("INVALID_REGO", 422, err.Error())
	}
	if err := s.repo.UpdateSource(ctx, s.db.Pool, id, source); err != nil {
		return err
	}
	s.loader.NotifyUpdate(id)
	return nil
}

func (s *service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, s.db.Pool, id); err != nil {
		return err
	}
	s.loader.NotifyUpdate(id)
	return nil
}

// ─── Policy sets ────────────────────────────────────────────────────────────

func (s *service) CreatePolicySet(ctx context.Context, input CreatePolicySetInput) (*PolicySet, error) {
	if input.OrgID == uuid.Nil || input.Name == "" {
		return nil, domainerr.ErrValidation
	}
	ps := &PolicySet{
		ID:        uuid.New(),
		OrgID:     input.OrgID,
		Name:      input.Name,
		CreatedAt: time.Now(),
	}
	if err := s.repo.CreatePolicySet(ctx, s.db.Pool, ps); err != nil {
		return nil, err
	}
	return ps, nil
}

func (s *service) AddToSet(ctx context.Context, setID, policyID uuid.UUID) error {
	return s.repo.AddToSet(ctx, s.db.Pool, setID, policyID)
}

func (s *service) RemoveFromSet(ctx context.Context, setID, policyID uuid.UUID) error {
	return s.repo.RemoveFromSet(ctx, s.db.Pool, setID, policyID)
}

func (s *service) BindSet(ctx context.Context, setID uuid.UUID, resourceType string, resourceID uuid.UUID) (*PolicySetBinding, error) {
	if resourceType != "ORG" && resourceType != "SPACE" && resourceType != "STACK" {
		return nil, domainerr.New("INVALID_RESOURCE_TYPE", 422, "resource_type must be ORG, SPACE, or STACK")
	}
	b, err := s.repo.BindSet(ctx, s.db.Pool, setID, resourceType, resourceID)
	if err != nil {
		return nil, err
	}
	s.loader.NotifyUpdate(uuid.Nil) // full reload
	return b, nil
}

func (s *service) UnbindSet(ctx context.Context, bindingID uuid.UUID) error {
	if err := s.repo.UnbindSet(ctx, s.db.Pool, bindingID); err != nil {
		return err
	}
	s.loader.NotifyUpdate(uuid.Nil)
	return nil
}

// ─── Evaluation ─────────────────────────────────────────────────────────────

func (s *service) Evaluate(ctx context.Context, input EvaluationInput) (*EvaluationResult, error) {
	return s.eval.Evaluate(ctx, input)
}

// ─── Dry run ────────────────────────────────────────────────────────────────

func (s *service) DryRun(ctx context.Context, input DryRunInput) (*EvaluationResult, error) {
	if input.OrgID == uuid.Nil || input.StackID == uuid.Nil {
		return nil, domainerr.ErrValidation
	}
	var planCtx *PlanContext
	if input.PlanJSON != "" {
		planCtx = &PlanContext{}
		if err := json.Unmarshal([]byte(input.PlanJSON), planCtx); err != nil {
			return nil, domainerr.New("INVALID_PLAN_JSON", 422, fmt.Sprintf("invalid plan_json: %v", err))
		}
	}
	evalInput := EvaluationInput{
		OrgID:      input.OrgID,
		StackID:    input.StackID,
		SpaceID:    input.SpaceID,
		RunType:    "dry_run",
		Actor:      ActorContext{ID: uuid.Nil, Type: "SYSTEM", Roles: []string{"dry_run"}},
		Stack:      StackContext{Name: "dry-run", Labels: map[string]string{}, Space: "dry-run"},
		PlanOutput: planCtx,
	}
	return s.eval.Evaluate(ctx, evalInput)
}

// ─── Validation ─────────────────────────────────────────────────────────────

// validateRego checks that the Rego source is syntactically valid and defines
// the expected query path.
func validateRego(source string, name string) error {
	if _, err := ast.ParseModuleWithOpts(name+".rego", source, ast.ParserOptions{
		ProcessAnnotation: true,
	}); err != nil {
		return fmt.Errorf("invalid Rego syntax: %w", err)
	}
	mod, err := ast.ParseModule(name+".rego", source)
	if err != nil {
		return fmt.Errorf("invalid Rego syntax: %w", err)
	}
	if mod == nil {
		return fmt.Errorf("empty Rego module")
	}
	if !hasRule(mod, "stratum", "policy") {
		return fmt.Errorf("policy must define under 'package stratum.policy'")
	}
	if !hasDenyRule(mod) {
		return fmt.Errorf("policy must define a rule named 'deny'")
	}
	return nil
}

// hasDenyRule checks whether a Rego module has at least one rule named "deny".
func hasDenyRule(mod *ast.Module) bool {
	for _, rule := range mod.Rules {
		if rule.Head.Name.String() == "deny" {
			return true
		}
	}
	return false
}

// hasRule checks whether a Rego module's package path matches the given
// segments. pkg should NOT include the implicit 'data' root.
func hasRule(mod *ast.Module, pkg ...string) bool {
	if mod == nil || mod.Package == nil || len(pkg) == 0 {
		return false
	}
	pkgPath := mod.Package.Path
	// pkgPath includes implicit 'data' root, so we expect len(pkg)+1
	if len(pkgPath) != len(pkg)+1 {
		return false
	}
	for i, name := range pkg {
		// Compare value directly (avoids quoting differences)
		if s, ok := pkgPath[i+1].Value.(ast.String); ok {
			if string(s) != name {
				return false
			}
		} else if v, ok := pkgPath[i+1].Value.(ast.Var); ok {
			if string(v) != name {
				return false
			}
		} else {
			return false
		}
	}
	return true
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func boolPtr(b bool) *bool { return &b }

func enforcementPtr(e EnforcementLevel) *EnforcementLevel { return &e }
