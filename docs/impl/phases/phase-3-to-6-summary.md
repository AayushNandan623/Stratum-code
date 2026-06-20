# Phase 3: Worker Runtime

## Scope

**IN scope:**
- Worker registration and heartbeat API (`/api/v1/internal/workers/*`)
- Job claim endpoint (long-poll: `GET /api/v1/internal/workers/{id}/jobs?timeout=30`)
- Run event ingestion from workers (`POST /api/v1/internal/runs/{id}/events`)
- Log chunk ingestion from workers (`POST /api/v1/internal/logs/{id}`)
- Source archive generation (git archive served to worker)
- Secret one-time claim endpoint
- Worker agent binary (`cmd/stratum-worker/main.go`)
- Docker executor (`internal/worker/docker.go`) — creates container, streams logs, reports result
- Hosted worker pool (control plane launches Docker containers on-demand)
- Worker pool management API

**OUT of scope:**
- Policy evaluation during execution (Phase 4 — stub returns PASS)
- Kubernetes executor (future)
- NATS-based dispatch (Phase 2 stretch)

---

## Key Interface: Executor

```go
// internal/worker/executor.go
type Executor interface {
    Execute(ctx context.Context, task *ExecutionTask) (*ExecutionResult, error)
}

type ExecutionTask struct {
    RunID       uuid.UUID
    StackID     uuid.UUID
    RunType     string     // plan | apply | destroy
    WorkDir     string     // path to extracted source
    IACTool     string     // opentofu
    IACVersion  string
    Env         []EnvVar   // injected secrets + TF_VARs
    LogCallback func(line string, source string)
}

type ExecutionResult struct {
    ExitCode    int
    PlanOutput  []byte     // plan.json if run_type=plan
    StateOutput []byte     // tfstate if run_type=apply
    Error       string
}
```

---

## Worker Agent Flow

```
1. Read config: STRATUM_WORKER_TOKEN, STRATUM_CONTROL_PLANE_URL
2. POST /api/v1/internal/workers/register → get worker_id
3. Loop:
   a. GET /api/v1/internal/workers/{id}/jobs?timeout=30  (long-poll)
   b. If job received: run execution protocol (see architecture/04-worker-model.md)
   c. On completion: POST /api/v1/internal/runs/{id}/events {type: run.applied | run.failed}
   d. Return to step 3a
4. Goroutine: POST /api/v1/internal/workers/{id}/heartbeat every 15s
5. On SIGTERM: finish current job if in PLANNING, abort if in APPLYING (mark failed), deregister
```

---

## Docker Execution Notes

- Use Docker SDK for Go (`github.com/docker/docker/client`)
- Pull image if not present (with retry + timeout)
- Container stdin: closed
- Container stdout/stderr: streamed line by line via `ContainerLogs` with `Follow: true`
- Each log line POSTed to control plane as it arrives (batch 10 lines max, 500ms max wait)
- Container cleanup: always `RemoveContainer` in defer (force=true)
- Execution timeout: configurable per pool (default: 30 min), enforced via context deadline

---

## Validation Criteria

After Phase 3:
1. Start `stratum-worker` with valid token → appears as IDLE worker in API
2. Create a run → within 30s, worker claims it, Docker container starts
3. For a simple Terraform config (local null resource), run completes as APPLIED
4. Run logs visible via API and WebSocket
5. Kill worker mid-plan → within 60s, run re-queues → new worker picks up
6. Kill worker mid-apply → run moves to FAILED (not re-queued)

---
---

# Phase 4: Policy Engine

## Scope

**IN scope:**
- OPA Go SDK integration (`github.com/open-policy-agent/opa/v1`)
- Policy CRUD API (create, update, delete policy definitions per org)
- Policy bundle loading from DB at startup + hot-reload on update
- Pre-run policy evaluation (called by run scheduler before moving PLANNED → APPLYING)
- Policy evaluation result stored as `policy.evaluated` run event
- Built-in policy library (no-public-buckets, require-tags, resource-limits)
- Policy input document construction from run + stack + plan context

**OUT of scope:**
- External OPA server
- Policy testing framework (future)
- Cost policy (future)

---

## Policy Evaluation Integration Point

In Phase 2, the scheduler's transition from PLANNED to APPLYING skips policy (stub). In Phase 4, replace the stub:

```go
// internal/run/scheduler.go — in the PLANNED → APPLYING transition
verdict, err := s.policyService.Evaluate(ctx, PolicyEvaluationInput{
    RunID:   run.ID,
    OrgID:   run.OrgID,
    StackID: run.StackID,
    PlanOutput: planOutput,  // loaded from run's plan artifact
})
if verdict.Severity == HardFail {
    s.transitionRun(ctx, run, StatePolicyRejected, verdict)
} else {
    s.transitionRun(ctx, run, StateApplying, nil)
}
```

---

## Validation Criteria

After Phase 4:
1. Create a policy rule: deny runs where `total_changes > 10`
2. Create a run on a stack with >10 planned changes → run moves to POLICY_REJECTED
3. Run timeline shows `policy.evaluated` event with violation details
4. Disable the policy → same run proceeds to APPLYING
5. Soft-warn policy → run proceeds with warning event recorded

---
---

# Phase 5: Reconciliation

## Scope

**IN scope:**
- Reconcile schedule table management
- Reconciler controller goroutine (priority queue, SKIP LOCKED claim)
- Drift-detect run creation (type=DRIFT_DETECT)
- Drift record creation and management
- Stack status transitions driven by drift (ACTIVE ↔ DRIFTED)
- Remediation mode handling (NONE / NOTIFY / AUTO_PLAN / AUTO_APPLY)
- Reconcile schedule API (enable/disable/configure per stack)
- Manual reconcile trigger API

**OUT of scope:**
- NATS-based event triggers (Phase 6)

---

## Reconciler Controller

```go
// internal/reconcile/controller.go
type Controller struct {
    repo      *ReconcileRepository
    runSvc    run.RunService
    stackSvc  stack.StackService
    clock     clock.Clock
    workers   int  // goroutine pool size (default: 5)
}

func (c *Controller) Start(ctx context.Context) {
    // Goroutine pool: workers goroutines each running checkLoop
    for i := 0; i < c.workers; i++ {
        go c.checkLoop(ctx)
    }
}

func (c *Controller) checkLoop(ctx context.Context) {
    for {
        stack := c.repo.ClaimNextDue(ctx)  // SKIP LOCKED, returns nil if nothing due
        if stack == nil {
            time.Sleep(10 * time.Second)
            continue
        }
        c.checkStack(ctx, stack)
    }
}
```

---

## Validation Criteria

After Phase 5:
1. Stack with reconcile_interval=1min → drift-detect run created automatically
2. Simulate drift by modifying state file directly → drift record created, stack moves to DRIFTED
3. Stack with drift_mode=AUTO_PLAN → plan run automatically queued after drift detected
4. Manual reconcile trigger via API → immediate drift-detect run

---
---

# Phase 6: Event Sourcing + NATS

## Scope

**IN scope:**
- NATS JetStream setup (streams and consumers defined in code)
- Outbox relay goroutine (DB outbox → NATS publish)
- NATS-backed EventBus replacing in-memory hub
- WebSocket broadcast via NATS (multi-instance safe)
- Audit log archiver consumer
- Notification router consumer (Slack webhook stub)
- Replace PostgreSQL long-poll job queue with NATS dispatch (optional — evaluate at Phase 6)

**OUT of scope:**
- Temporal integration (post-Phase 6)
- Full event replay API (post-Phase 6)

---

## NATS Setup

```go
// internal/events/nats.go
// Streams defined:
//   STRATUM_RUNS    subjects: stratum.runs.events.>   retention: limits, 30d
//   STRATUM_STACKS  subjects: stratum.stacks.events.> retention: limits, 90d
//   STRATUM_AUDIT   subjects: stratum.audit.>         retention: limits, 1y

// Consumers:
//   websocket-fan-out    push consumer, delivers to each API instance
//   audit-archiver       pull consumer, writes to audit_log table
//   notification-router  pull consumer, sends Slack/email
```

---

## Outbox Relay

```go
// internal/events/outbox.go
type OutboxRelay struct {
    db   *pgxpool.Pool
    bus  EventBus
    tick time.Duration  // 1s default
}

func (r *OutboxRelay) Start(ctx context.Context) {
    for {
        select {
        case <-time.After(r.tick):
            r.flush(ctx)
        case <-ctx.Done():
            return
        }
    }
}

func (r *OutboxRelay) flush(ctx context.Context) {
    // SELECT FOR UPDATE SKIP LOCKED pending outbox messages
    // Publish each to NATS
    // Mark DELIVERED or increment attempt on failure
}
```

---

## Validation Criteria

After Phase 6:
1. Start two API server instances. Connect WebSocket on instance 1 for a run. Create run event via instance 2. Verify WebSocket on instance 1 receives the event.
2. All run events appear in audit_log table via the archiver consumer
3. Outbox: kill NATS, create run event, restart NATS → event eventually delivered
