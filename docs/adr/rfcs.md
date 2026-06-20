# RFC Structure

RFCs (Request for Comments) propose significant new features or changes to Stratum's architecture. They are written before implementation begins and require design review.

## When to Write an RFC

Write an RFC when:
- Adding a new bounded context
- Changing the run state machine (new states or transitions)
- Introducing a new external dependency
- Changing the worker protocol in a breaking way
- Proposing a multi-phase feature that affects multiple contexts
- Changing the event schema in a backward-incompatible way

Do NOT write an RFC for:
- Bug fixes
- New API endpoints that fit existing patterns
- New built-in OPA policies
- Performance optimizations within a single module
- Documentation changes

---

## RFC Index

| # | Title | Status | Author | Phase |
|---|-------|--------|--------|-------|
| RFC-001 | Progressive Infrastructure Rollouts | Draft | — | Post-v1 |
| RFC-002 | Temporal Workflow Engine Integration | Draft | — | Post-v1 |
| RFC-003 | Multi-IaC Tool Support (Pulumi, Ansible) | Draft | — | Post-v1 |
| RFC-004 | Ephemeral Stack Environments | Draft | — | Post-v1 |
| RFC-005 | Cost Estimation Integration | Proposal | — | Post-v1 |

---

## RFC Template

```markdown
# RFC-NNN: [Title]

**Status:** Draft | In Review | Accepted | Rejected | Superseded
**Author:** [name]
**Created:** YYYY-MM-DD
**Target Phase:** Phase N or Post-vX

---

## Summary

One paragraph: what is being proposed and why.

---

## Motivation

Why does this need to exist? What problem does it solve?
What is the current workaround (if any) and why is it insufficient?

---

## Detailed Design

The full proposed design. Include:
- New domain concepts (if any)
- New or modified bounded contexts
- New or modified state machines
- New API endpoints
- New DB tables or schema changes
- New event types
- Worker protocol changes (if any)

Diagrams are encouraged.

---

## Alternatives Considered

What other approaches were considered? Why were they rejected?

---

## Implementation Plan

Which phases does this span? What is the sequencing?
Does this require any migration of existing data?

---

## Drawbacks

What are the downsides of this proposal?
What complexity does it introduce?
What operational burden does it add?

---

## Open Questions

List any unresolved questions that need answers before acceptance.
```

---

## RFC-001: Progressive Infrastructure Rollouts (Draft)

**Status:** Draft
**Target Phase:** Post-v1

### Summary
Add a `RolloutStrategy` concept to stacks that allows infrastructure changes to be applied progressively across multiple targets (regions, environments, or account groups) with automated health gates between each stage.

### Motivation
Currently, when a stack's apply run completes, the change is applied to all target infrastructure simultaneously. For large-scale changes (e.g., a security group rule change affecting 500 instances across 3 regions), this is all-or-nothing. A production incident discovered mid-apply cannot be automatically halted.

Progressive rollouts allow: apply to `us-east-1` → evaluate health → apply to `us-west-2` → evaluate health → apply to `eu-west-1`. Failure at any stage halts the rollout and optionally triggers rollback.

### Key Design Concepts

**RolloutPlan:** A sequence of `stages`, each targeting a subset of the infrastructure defined by labels or resource addresses.

**Stage:** One unit of a rollout. Contains: target filter (e.g., `region=us-east-1`), the IaC scope (a `-target` argument to OpenTofu), and a health gate (wait N minutes, run a test command, or call a webhook).

**RolloutRun:** A new run type that orchestrates multiple `StageRun`s. The `RolloutRun` itself is a parent run; each stage produces a child `apply` run targeting a subset.

### New State Machine (RolloutRun)
```
PENDING → STAGE_1_RUNNING → STAGE_1_HEALTH_CHECK → STAGE_2_RUNNING → ... → COMPLETED
                                                   ↓ (gate failure)
                                              ROLLING_BACK → ROLLED_BACK
```

### Open Questions
1. How are rollout targets defined for non-Kubernetes infrastructure? (`-target` in Terraform is fragile)
2. What health gate mechanisms are supported in v1? (webhook-only? CloudWatch alarm check?)
3. How does rollback work for already-applied stages? (new destroy run per stage?)

---

## RFC-002: Temporal Workflow Engine Integration (Draft)

**Status:** Draft
**Target Phase:** Post-v1 (Phase 7)

### Summary
Replace the PostgreSQL-based run scheduler and job queue with Temporal workflows, gaining durable execution, automatic retries, and sophisticated multi-step workflow semantics at the cost of a new infrastructure dependency.

### Motivation
The PostgreSQL scheduler works well for simple plan/apply/drift-detect runs. It becomes complex for:
- Multi-stage rollout orchestration (RFC-001)
- Cross-stack orchestration with complex wait conditions
- Long-running approval workflows (days-long timeouts without polling)
- Guaranteed exactly-once execution of side-effectful operations

Temporal solves all of these natively. Its activity/workflow model maps directly to Stratum's run execution model.

### Migration Strategy
The `WorkflowEngine` interface (stubbed in Phase 2, PostgreSQL-backed in Phase 2-6) gets a Temporal implementation. Existing in-flight runs migrate via a cutover window (drain existing PG-queued runs, then switch).

### Interface (already designed for this migration)
```go
type WorkflowEngine interface {
    StartRun(ctx context.Context, run *Run) (WorkflowHandle, error)
    CancelRun(ctx context.Context, runID uuid.UUID) error
    GetStatus(ctx context.Context, runID uuid.UUID) (*WorkflowStatus, error)
}
```

### Drawbacks
- Temporal requires running a Temporal server cluster (significant operational overhead)
- Temporal's Go SDK adds compile-time constraints (workflow code must be deterministic)
- Migration of in-flight runs requires careful cutover planning

### Decision Criterion
Implement Temporal when: the PostgreSQL scheduler shows measurable reliability issues with multi-stage workflows, OR when RFC-001 (Progressive Rollouts) requires it.

---

## RFC-003: Multi-IaC Tool Support (Draft)

**Status:** Draft
**Target Phase:** Post-v1

### Summary
Extend the worker executor to support Pulumi (TypeScript/Python/Go) and Ansible in addition to OpenTofu, with a pluggable `ToolRuntime` interface.

### Design
The `Executor` interface already abstracts tool execution. A new `ToolRuntime` interface is introduced:

```go
type ToolRuntime interface {
    Name() string          // "opentofu" | "pulumi" | "ansible"
    ContainerImage() string
    BuildCommand(task *ExecutionTask) []string
    ParseOutput(raw []byte) (*ExecutionResult, error)
    StateBackendEnv(stackID uuid.UUID) []EnvVar
}
```

Each IaC tool has its own `ToolRuntime` implementation. The `DockerExecutor` takes a `ToolRuntime` at construction time. Stack configuration gains an `iac_tool` field (already in schema: `iac_tool VARCHAR(32) DEFAULT 'opentofu'`).

### Pulumi-specific considerations
- Pulumi uses a different state model (Pulumi Cloud or S3 backend)
- `pulumi preview` replaces `tofu plan`; output format differs
- Pulumi stacks are tied to a Pulumi organization — credentials are different

### Ansible-specific considerations
- Ansible has no state file — reconciliation is run-to-completion only
- No `plan` phase — only `check` mode (equivalent of dry-run)
- Inventory management is stack-specific

### Open Questions
1. How is the plan output parsed for Pulumi/Ansible to extract resource changes for policy evaluation?
2. Does Pulumi's state backend integrate with Stratum's State context, or is it fully external?
