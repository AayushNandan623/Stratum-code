# Implementation Guide for AI Coding Agents

## How to Use This Documentation

You are an AI coding agent implementing a specific phase or module of Stratum. Follow these rules exactly.

---

## Rule 1: Load Only What You Need

Before starting any task, identify your scope:
- Implementing a specific phase → load only `impl/phases/phase-N-*.md`
- Implementing a specific module → load only `impl/modules/<module>.md`
- Debugging a cross-context issue → load the relevant bounded context docs from `docs/architecture/`

**Do NOT load all documentation.** Each phase document is self-contained with enough context to implement that phase.

---

## Rule 2: Phase Sequence is Mandatory

Phases must be implemented in order. Phase N assumes Phase N-1 is complete and working.

```
Phase 0 → Phase 1 → Phase 2 → Phase 3 → Phase 4 → Phase 5 → Phase 6
```

Do not implement Phase 2 unless Phase 0 and Phase 1 are complete.

---

## Rule 3: Interface First, Implementation Second

Each phase document defines interfaces (Go interfaces, API contracts, DB schemas). Implement interfaces before implementations. This allows other modules to compile and test against interfaces even before full implementation exists.

---

## Rule 4: No Cross-Phase Imports

If you are implementing Phase 2 (Run Orchestration), do not implement Phase 4 features (Policy Engine). If Phase 2 needs a policy check, call a stub interface that returns `allow: true` until Phase 4 exists.

---

## Rule 5: File Size Constraint

No single Go file exceeds 400 lines. If a file approaches this limit, extract to a new file. Repository files (`repository.go`) split by entity if needed. Service files split by responsibility.

This is not an aesthetic rule — it is a context window constraint. Future AI agents must be able to load a single file without context overflow.

---

## Phase Document Format

Each phase document contains:

```
## Scope
  What this phase implements. What is explicitly OUT of scope.

## Prerequisites  
  What must exist before starting this phase (from previous phases).

## Files to Create
  Exact file paths and their purpose.

## DB Schema
  SQL for new tables. Migration file name.

## Interfaces
  Go interface definitions (source of truth for this phase's contracts).

## Implementation Notes
  Key algorithms, patterns, and constraints.
  NOT a full implementation — enough to guide a correct implementation.

## Validation Criteria
  How to verify this phase is correctly implemented.
  Curl commands, expected DB state, etc.
```

---

## Context Optimization Strategy

When implementing a phase:

**Minimal required context:**
1. This file (`impl/README.md`) — once, for orientation
2. The specific phase document — always
3. `REPOSITORY.md` — for file paths and import rules
4. The `types.go` of any context you're implementing — for domain model

**Load only on demand:**
- Architecture docs → only if you need deep conceptual understanding
- ADRs → only if you're questioning a design decision
- Other phase docs → only if resolving a dependency question

**Never load:**
- Completed phases' implementation files (trust the interfaces)
- All architecture docs simultaneously
- The full docs/ tree

---

## Common Implementation Patterns

### Repository Pattern
```go
// All DB access goes through a Repository type
// Repository methods take a context.Context as first arg
// Repository methods accept a db transaction (*pgxpool.Tx) for transactional operations
type StackRepository struct { db *pgxpool.Pool }
func (r *StackRepository) GetByID(ctx context.Context, id uuid.UUID, orgID uuid.UUID) (*Stack, error)
func (r *StackRepository) Create(ctx context.Context, tx pgx.Tx, s *Stack) error
```

### Service Pattern
```go
// Service wraps repository + business logic
// Service interfaces are defined for cross-context use
type StackService interface {
    Get(ctx context.Context, id uuid.UUID) (*Stack, error)
    Create(ctx context.Context, input CreateStackInput) (*Stack, error)
}
type stackService struct {
    repo   *StackRepository
    events events.EventBus
}
```

### Error Handling
```go
// Domain errors are typed, not strings
var ErrStackNotFound = &DomainError{Code: "STACK_NOT_FOUND", HTTPStatus: 404}
var ErrStackLocked   = &DomainError{Code: "STACK_LOCKED", HTTPStatus: 409}
// Wrap infra errors: fmt.Errorf("stackService.Get: %w", err)
```

### Configuration
```go
// All config from environment variables, loaded at startup
// No config files in production — use env vars
// Use internal/platform/config package
```

---

## Database Conventions

- UUIDs for all primary keys (`gen_random_uuid()` default)
- `created_at TIMESTAMPTZ NOT NULL DEFAULT now()`
- `updated_at TIMESTAMPTZ NOT NULL DEFAULT now()` (updated via trigger)
- `org_id UUID NOT NULL` on every tenant-scoped table
- Soft delete via `deleted_at TIMESTAMPTZ NULL` — no hard deletes
- All migrations are forward-only (no down migrations in production)

---

## Testing Conventions (Minimal)

Phase 0-3 testing priority:
1. Integration tests for state machine transitions (run states)
2. Integration tests for scheduler correctness (DAG ordering)
3. Unit tests for crypto (secrets), policy evaluation, cycle detection

Use `internal/testhelpers/dbtest.go` for DB tests. Each test runs in a transaction that is rolled back on cleanup. No test database creation/destruction per test.

No mocking frameworks. Use interface substitution with simple stub implementations.
