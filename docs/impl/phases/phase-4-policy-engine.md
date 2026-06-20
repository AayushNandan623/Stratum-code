# Phase 4: Policy Engine

## Scope

**IN scope:**
- `internal/policy/` package: service, repository, evaluator, bundle loader
- OPA Go SDK integration (embedded, in-process)
- Policy CRUD API (create, update, delete, list policies per org)
- Policy set management (group policies, attach to stacks/spaces/org)
- Policy bundle hot-reload (no restart required on policy update)
- Pre-run evaluation gate (replaces Phase 2-3 stub)
- Evaluation result stored as `policy.evaluated` run event
- Built-in policy library (3 starter policies)
- Policy dry-run endpoint (evaluate a policy against a hypothetical input)

**OUT of scope:**
- Policy testing framework/CI
- Cost-based policies (require external pricing API)
- Conftest/Checkov integration (post-Phase 4)
- Policy version rollback UI

---

## Prerequisites

Phase 3 complete. Workers can execute runs end-to-end. Plan output (`plan.json`) is stored by the control plane after a plan run.

---

## Files to Create

```
internal/policy/
  types.go          Policy, PolicySet, PolicyBinding, EvaluationResult domain types
  service.go        PolicyService interface + implementation
  repository.go     DB queries: policy CRUD, policy set management, result storage
  evaluator.go      OPA evaluation logic, input document construction
  loader.go         Bundle loading from DB, hot-reload via channel notification
  builtin/
    no_public_storage.rego
    require_resource_tags.rego
    resource_change_limit.rego

internal/api/handlers/
  policies.go       Policy management API handlers

internal/run/
  scheduler.go      MODIFY: replace policy stub with real PolicyService call
```

---

## DB Schema Additions

```sql
-- Migration: 011_init_policy.sql

CREATE TABLE policies (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        UUID NOT NULL REFERENCES organizations(id),
  name          VARCHAR(255) NOT NULL,
  description   TEXT,
  rego_source   TEXT NOT NULL,    -- raw Rego policy text
  enabled       BOOLEAN NOT NULL DEFAULT true,
  enforcement   VARCHAR(20) NOT NULL DEFAULT 'SOFT_WARN',  -- HARD_FAIL | SOFT_WARN
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at    TIMESTAMPTZ,
  UNIQUE (org_id, name)
);

CREATE TABLE policy_sets (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  name        VARCHAR(255) NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);

CREATE TABLE policy_set_members (
  policy_set_id UUID NOT NULL REFERENCES policy_sets(id),
  policy_id     UUID NOT NULL REFERENCES policies(id),
  PRIMARY KEY (policy_set_id, policy_id)
);

CREATE TABLE policy_set_bindings (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  policy_set_id   UUID NOT NULL REFERENCES policy_sets(id),
  resource_type   VARCHAR(20) NOT NULL,  -- ORG | SPACE | STACK
  resource_id     UUID NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Note: ORG-level binding applies to all stacks in org.
-- SPACE binding applies to all stacks in space.
-- STACK binding applies to one stack.
-- Effective policies for a run = union of org + space + stack bindings.
```

---

## OPA Evaluator Interface

```go
// internal/policy/evaluator.go

type Evaluator interface {
    Evaluate(ctx context.Context, input EvaluationInput) (*EvaluationResult, error)
}

type EvaluationInput struct {
    RunID      uuid.UUID
    OrgID      uuid.UUID
    StackID    uuid.UUID
    SpaceID    *uuid.UUID
    RunType    string
    Actor      ActorContext
    Stack      StackContext
    PlanOutput *PlanContext   // nil for plan-only runs (only evaluated on apply)
}

type ActorContext struct {
    ID    uuid.UUID
    Type  string   // USER | API_KEY | SYSTEM
    Roles []string
}

type StackContext struct {
    Name   string
    Labels map[string]string
    Space  string
}

type PlanContext struct {
    ResourceChanges []ResourceChange
    TotalAdded      int
    TotalChanged    int
    TotalRemoved    int
}

type EvaluationResult struct {
    Allow      bool
    Severity   PolicySeverity  // HARD_FAIL | SOFT_WARN
    Violations []PolicyViolation
    PolicyIDs  []uuid.UUID     // which policies were evaluated
    DurationMs int64
}

type PolicyViolation struct {
    PolicyID   uuid.UUID
    PolicyName string
    Message    string
    Resource   string  // Terraform resource address
}
```

---

## OPA Evaluation Implementation

```go
// internal/policy/evaluator.go

type OPAEvaluator struct {
    loader *BundleLoader
}

func (e *OPAEvaluator) Evaluate(ctx context.Context, input EvaluationInput) (*EvaluationResult, error) {
    start := time.Now()

    // 1. Get current policies for this run's scope
    policies := e.loader.GetPoliciesForScope(input.OrgID, input.SpaceID, input.StackID)
    if len(policies) == 0 {
        return &EvaluationResult{Allow: true}, nil
    }

    // 2. Build OPA input document
    opaInput := buildInputDocument(input)

    // 3. Evaluate each policy
    var violations []PolicyViolation
    hardFail := false

    for _, policy := range policies {
        if !policy.Enabled { continue }

        // Compile Rego module
        compiler := ast.MustCompileModules(map[string]string{
            policy.Name: policy.RegoSource,
        })

        // Evaluate: we expect policies to define `deny[msg]` or `warn[msg]`
        r := rego.New(
            rego.Query("data.stratum.policy"),
            rego.Compiler(compiler),
            rego.Input(opaInput),
        )
        rs, err := r.Eval(ctx)
        if err != nil {
            return nil, fmt.Errorf("policy %s eval error: %w", policy.Name, err)
        }

        denials := extractDenials(rs)
        for _, msg := range denials {
            violations = append(violations, PolicyViolation{
                PolicyID:   policy.ID,
                PolicyName: policy.Name,
                Message:    msg,
            })
            if policy.Enforcement == EnforcementHardFail {
                hardFail = true
            }
        }
    }

    return &EvaluationResult{
        Allow:      !hardFail,
        Severity:   severityFrom(hardFail),
        Violations: violations,
        PolicyIDs:  policyIDs(policies),
        DurationMs: time.Since(start).Milliseconds(),
    }, nil
}
```

---

## Policy Rego Convention

All Stratum policies must follow this convention:

```rego
# Policy: no public S3 buckets
# File: no_public_storage.rego

package stratum.policy

import future.keywords.if
import future.keywords.in

# deny[msg] defines violations
# Stratum evaluates data.stratum.policy.deny
deny[msg] if {
    change := input.plan.resource_changes[_]
    change.type == "aws_s3_bucket"
    change.after.acl == "public-read"
    msg := sprintf("S3 bucket '%s' has public-read ACL", [change.address])
}

deny[msg] if {
    change := input.plan.resource_changes[_]
    change.type == "aws_s3_bucket"
    change.after.acl == "public-read-write"
    msg := sprintf("S3 bucket '%s' has public-read-write ACL", [change.address])
}
```

The evaluator queries `data.stratum.policy.deny`. An empty set = pass. Non-empty = violations.

**Input document structure** (what `input` resolves to in Rego):
```json
{
  "run": { "id": "...", "type": "apply" },
  "stack": { "name": "prod-vpc", "labels": {"env": "production"}, "space": "production" },
  "plan": {
    "resource_changes": [
      {
        "address": "aws_s3_bucket.data",
        "type": "aws_s3_bucket",
        "actions": ["create"],
        "after": { "bucket": "my-bucket", "acl": "public-read" }
      }
    ],
    "total_added": 1, "total_changed": 0, "total_removed": 0
  },
  "actor": { "id": "...", "type": "USER", "roles": ["stack:writer"] },
  "organization": { "id": "..." }
}
```

---

## Bundle Loader and Hot-Reload

```go
// internal/policy/loader.go

type BundleLoader struct {
    mu       sync.RWMutex
    policies map[uuid.UUID]*Policy   // keyed by policy ID
    byScope  map[string][]*Policy    // keyed by "org:{id}", "space:{id}", "stack:{id}"
    repo     *PolicyRepository
    updateCh chan uuid.UUID           // notified when a policy is updated
}

func (l *BundleLoader) Start(ctx context.Context) {
    // Initial load
    l.reload(ctx)
    // Watch for updates
    for {
        select {
        case policyID := <-l.updateCh:
            l.reloadPolicy(ctx, policyID)
        case <-time.After(5 * time.Minute):
            l.reload(ctx)  // full refresh every 5 min as safety net
        case <-ctx.Done():
            return
        }
    }
}

// Called by PolicyService.Update() after DB write
func (l *BundleLoader) NotifyUpdate(policyID uuid.UUID) {
    l.updateCh <- policyID
}

func (l *BundleLoader) GetPoliciesForScope(orgID uuid.UUID, spaceID *uuid.UUID, stackID uuid.UUID) []*Policy {
    l.mu.RLock()
    defer l.mu.RUnlock()
    // Union org + space + stack policies, deduplicated by ID
    seen := map[uuid.UUID]bool{}
    var result []*Policy
    for _, p := range l.byScope[fmt.Sprintf("org:%s", orgID)] {
        if !seen[p.ID] { result = append(result, p); seen[p.ID] = true }
    }
    if spaceID != nil {
        for _, p := range l.byScope[fmt.Sprintf("space:%s", *spaceID)] {
            if !seen[p.ID] { result = append(result, p); seen[p.ID] = true }
        }
    }
    for _, p := range l.byScope[fmt.Sprintf("stack:%s", stackID)] {
        if !seen[p.ID] { result = append(result, p); seen[p.ID] = true }
    }
    return result
}
```

---

## Scheduler Integration (Modifies Phase 2 Code)

In `internal/run/scheduler.go`, replace the policy stub in the `PLANNED → next-state` transition:

```go
// In scheduler.go tick, when a run is in PLANNED state:

func (s *Scheduler) evaluatePolicyGate(ctx context.Context, run *Run) error {
    planOutput := s.runRepo.GetPlanOutput(ctx, run.ID) // retrieve stored plan.json

    verdict, err := s.policySvc.Evaluate(ctx, policy.EvaluationInput{
        RunID:      run.ID,
        OrgID:      run.OrgID,
        StackID:    run.StackID,
        SpaceID:    run.SpaceID,
        RunType:    string(run.RunType),
        Actor:      buildActorContext(run),
        Stack:      buildStackContext(ctx, run.StackID),
        PlanOutput: buildPlanContext(planOutput),
    })
    if err != nil {
        // Policy service error — do not fail the run, log and retry
        log.Error("policy evaluation error", "run_id", run.ID, "err", err)
        return err
    }

    // Store evaluation result as run event
    s.runSvc.AppendEvent(ctx, run.ID, RunEventInput{
        Type:    EventPolicyEvaluated,
        Payload: verdictToPayload(verdict),
    })

    if !verdict.Allow {
        return s.runSvc.Transition(ctx, run.ID, StatePolicyRejected)
    }

    // Soft warnings: proceed but record them
    nextState := s.determinePostPlanState(run, verdict)
    return s.runSvc.Transition(ctx, run.ID, nextState)
}
```

---

## Policy API Endpoints

```
POST   /api/v1/orgs/{org_id}/policies              Create policy (upload Rego source)
GET    /api/v1/orgs/{org_id}/policies              List policies
GET    /api/v1/policies/{policy_id}                Get policy + source
PATCH  /api/v1/policies/{policy_id}                Update name/description/enforcement/enabled
PUT    /api/v1/policies/{policy_id}/source         Replace Rego source (validates syntax before save)
DELETE /api/v1/policies/{policy_id}                Soft delete

POST   /api/v1/orgs/{org_id}/policy-sets           Create policy set
POST   /api/v1/policy-sets/{id}/members            Add policy to set
DELETE /api/v1/policy-sets/{id}/members/{policy_id} Remove from set
POST   /api/v1/policy-sets/{id}/bindings           Bind to org/space/stack
DELETE /api/v1/policy-sets/{id}/bindings/{binding_id}

POST   /api/v1/orgs/{org_id}/policies/dry-run      Evaluate policies against provided plan.json
  Body: { stack_id, plan_json: "..." }
  Response: EvaluationResult
```

---

## Rego Syntax Validation

Before saving a policy, validate the Rego source:

```go
func validateRego(source string, name string) error {
    _, err := ast.ParseModuleWithOpts(name, source, ast.ParserOptions{
        ProcessAnnotation: true,
    })
    if err != nil {
        return fmt.Errorf("invalid Rego syntax: %w", err)
    }
    // Also verify it defines the expected query path
    mod, _ := ast.ParseModule(name, source)
    if !hasRule(mod, "stratum", "policy", "deny") {
        return fmt.Errorf("policy must define 'deny[msg]' under 'package stratum.policy'")
    }
    return nil
}
```

---

## Validation Criteria

After Phase 4:
1. Create a `HARD_FAIL` policy: deny runs if `total_added > 5`
2. Run a stack with 6 new resources → run moves to `POLICY_REJECTED`
3. Run timeline shows `policy.evaluated` event with violation message
4. Disable the policy → same configuration run proceeds to `APPLYING`
5. Create a `SOFT_WARN` policy → run proceeds; timeline shows warning event
6. PUT invalid Rego source → API returns 422 with syntax error message
7. Policy dry-run endpoint returns evaluation result for a provided plan.json
8. Update policy source without restart → new evaluation immediately uses updated source (hot-reload)
9. Attach policy set to a space → all stacks in space evaluate it on next run
