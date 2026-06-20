# Phase 5: Reconciliation

## Scope

**IN scope:**
- `internal/reconcile/` package: service, repository, controller goroutine
- Reconcile schedule management (per-stack schedule CRUD)
- Drift detection controller (priority queue, SKIP LOCKED worker pool)
- Drift record lifecycle (DETECTED → REMEDIATING → RESOLVED / IGNORED)
- Stack status transitions: ACTIVE ↔ DRIFTED
- Remediation mode handling: NONE | NOTIFY | AUTO_PLAN | AUTO_APPLY
- Reconcile API endpoints
- Manual trigger endpoint
- Drift detection event emission (`stack.drifted`, `drift.resolved`)
- Integration with Run context (creates runs for drift-detect and remediation)
- Plan output parsing for drift determination

**OUT of scope:**
- NATS-based event triggers (Phase 6 adds event-driven triggers on top of schedule-based)
- Progressive rollout reconciliation (post-Phase 6)
- Cross-stack cascade reconciliation (post-Phase 6)

---

## Prerequisites

Phase 4 complete. Policy engine integrated. Workers can complete full plan/apply/drift-detect run lifecycle. Plan output parsing (resource_changes) is implemented in Phase 3.

---

## Files to Create / Modify

```
internal/reconcile/
  types.go          ReconcileSchedule, DriftRecord, DriftSummary domain types
  service.go        ReconcileService interface + implementation
  repository.go     DB queries: schedule management, drift record CRUD
  controller.go     Reconciler controller goroutine pool

internal/stack/
  service.go        MODIFY: add SetStatus(ctx, stackID, status) method
  types.go          MODIFY: add DRIFTED to StackStatus enum

internal/api/handlers/
  reconcile.go      Reconcile API handlers

cmd/stratum-server/
  main.go           MODIFY: start reconcile controller on startup
```

---

## DB Schema Additions

```sql
-- Migration: 012_init_reconcile.sql

CREATE TABLE reconcile_schedules (
  stack_id              UUID PRIMARY KEY REFERENCES stacks(id),
  org_id                UUID NOT NULL REFERENCES organizations(id),
  enabled               BOOLEAN NOT NULL DEFAULT true,
  reconcile_interval    INTERVAL NOT NULL DEFAULT '1 hour',
  drift_mode            VARCHAR(20) NOT NULL DEFAULT 'NOTIFY',
  next_check_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_check_at         TIMESTAMPTZ,
  last_drift_at         TIMESTAMPTZ,
  consecutive_failures  INT NOT NULL DEFAULT 0,
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reconcile_schedule_due
  ON reconcile_schedules (next_check_at)
  WHERE enabled = true;

CREATE TABLE drift_records (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  stack_id              UUID NOT NULL REFERENCES stacks(id),
  org_id                UUID NOT NULL REFERENCES organizations(id),
  trigger_run_id        UUID NOT NULL REFERENCES runs(id),  -- the drift-detect run
  status                VARCHAR(20) NOT NULL DEFAULT 'DETECTED',
  resource_count        INT NOT NULL DEFAULT 0,
  drift_summary         JSONB NOT NULL DEFAULT '{}',
  remediation_run_id    UUID REFERENCES runs(id),
  detected_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at           TIMESTAMPTZ,
  ignored_at            TIMESTAMPTZ,
  ignored_by            UUID REFERENCES users(id)
);

CREATE INDEX idx_drift_records_stack_id ON drift_records(stack_id, status);
CREATE INDEX idx_drift_records_org_id   ON drift_records(org_id, detected_at DESC);
```

---

## Key Interfaces

```go
// internal/reconcile/service.go

type ReconcileService interface {
    // Schedule management
    GetSchedule(ctx context.Context, stackID uuid.UUID) (*ReconcileSchedule, error)
    UpdateSchedule(ctx context.Context, stackID uuid.UUID, input UpdateScheduleInput) (*ReconcileSchedule, error)
    EnableSchedule(ctx context.Context, stackID uuid.UUID) error
    DisableSchedule(ctx context.Context, stackID uuid.UUID) error

    // Manual trigger
    TriggerNow(ctx context.Context, stackID uuid.UUID, actorID uuid.UUID) (*Run, error)

    // Drift record management
    GetDriftRecord(ctx context.Context, id uuid.UUID) (*DriftRecord, error)
    ListDriftRecords(ctx context.Context, filter DriftFilter) ([]*DriftRecord, int, error)
    IgnoreDrift(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error

    // Called by run context when drift-detect run completes
    ProcessDriftResult(ctx context.Context, runID uuid.UUID, planOutput *PlanOutput) error
}
```

---

## Controller Architecture

The controller uses a goroutine pool to process stacks in parallel. Each goroutine claims one stack at a time using `SKIP LOCKED`.

```go
// internal/reconcile/controller.go

type Controller struct {
    repo       *ReconcileRepository
    runSvc     run.RunService
    stackSvc   stack.StackService
    clock      clock.Clock
    poolSize   int  // default 5 goroutines
    logger     *slog.Logger
}

func (c *Controller) Start(ctx context.Context) {
    c.logger.Info("reconcile controller starting", "pool_size", c.poolSize)
    var wg sync.WaitGroup
    for i := 0; i < c.poolSize; i++ {
        wg.Add(1)
        go func(workerN int) {
            defer wg.Done()
            c.workerLoop(ctx, workerN)
        }(i)
    }
    wg.Wait()
    c.logger.Info("reconcile controller stopped")
}

func (c *Controller) workerLoop(ctx context.Context, n int) {
    backoff := 10 * time.Second
    for {
        select {
        case <-ctx.Done():
            return
        default:
        }

        schedule, err := c.repo.ClaimNextDue(ctx)
        if err != nil {
            c.logger.Error("claim error", "worker", n, "err", err)
            sleepWithJitter(ctx, backoff)
            continue
        }
        if schedule == nil {
            // Nothing due — sleep and retry
            sleepWithJitter(ctx, backoff)
            continue
        }

        backoff = 10 * time.Second // reset backoff on successful claim
        c.processStack(ctx, schedule)
    }
}
```

**Claim query:**
```sql
WITH claimed AS (
  SELECT stack_id FROM reconcile_schedules
  WHERE enabled = true
    AND next_check_at <= now()
  ORDER BY next_check_at
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
UPDATE reconcile_schedules
SET next_check_at = now() + reconcile_interval,
    last_check_at = now()
WHERE stack_id = (SELECT stack_id FROM claimed)
RETURNING *;
```

The `next_check_at` is advanced **before** the check runs. This means if the control plane crashes mid-check, the schedule still advances — preventing a runaway retry loop on a permanently-failing stack.

---

## Stack Processing Flow

```go
func (c *Controller) processStack(ctx context.Context, schedule *ReconcileSchedule) {
    log := c.logger.With("stack_id", schedule.StackID)

    // 1. Check if stack is eligible for drift detection
    stack, err := c.stackSvc.Get(ctx, schedule.StackID)
    if err != nil || stack.DeletedAt != nil {
        // Stack deleted — disable schedule
        c.repo.DisableSchedule(ctx, schedule.StackID)
        return
    }

    // 2. Skip if there's already an active run
    if c.runSvc.HasActiveRun(ctx, schedule.StackID) {
        log.Debug("skipping drift check: active run in progress")
        return
    }

    // 3. Create drift-detect run
    run, err := c.runSvc.Create(ctx, run.CreateRunInput{
        OrgID:       stack.OrgID,
        StackID:     stack.ID,
        RunType:     run.RunTypeDriftDetect,
        TriggerType: run.TriggerTypeSchedule,
        TriggeredBy: nil, // system-initiated
    })
    if err != nil {
        log.Error("failed to create drift-detect run", "err", err)
        c.repo.RecordFailure(ctx, schedule.StackID)
        return
    }

    log.Info("drift-detect run created", "run_id", run.ID)
    c.repo.ResetFailures(ctx, schedule.StackID)

    // Note: run execution and result processing happen asynchronously.
    // The run proceeds through the normal scheduler → worker → event flow.
    // ProcessDriftResult() is called by the run service when the run completes.
}
```

---

## Drift Result Processing

When a drift-detect run reaches `APPLIED` (or `FAILED`), the run service calls back into the reconcile service:

```go
// internal/reconcile/service.go

func (s *reconcileService) ProcessDriftResult(ctx context.Context, runID uuid.UUID, planOutput *run.PlanOutput) error {
    run, _ := s.runSvc.Get(ctx, runID)

    // Determine if drift was detected
    hasDrift := planOutput != nil && planOutput.HasChanges

    if !hasDrift {
        // Clean: ensure stack is in ACTIVE status
        s.stackSvc.SetStatus(ctx, run.StackID, stack.StatusActive)
        s.repo.UpdateLastDriftAt(ctx, run.StackID, nil)
        return nil
    }

    // Drift detected: create drift record
    summary := buildDriftSummary(planOutput)
    driftRecord, err := s.repo.CreateDriftRecord(ctx, &DriftRecord{
        StackID:       run.StackID,
        OrgID:         run.OrgID,
        TriggerRunID:  run.ID,
        Status:        DriftStatusDetected,
        ResourceCount: len(planOutput.ResourceChanges),
        DriftSummary:  summary,
    })
    if err != nil { return err }

    // Update stack status
    s.stackSvc.SetStatus(ctx, run.StackID, stack.StatusDrifted)
    s.repo.UpdateLastDriftAt(ctx, run.StackID, &driftRecord.DetectedAt)

    // Emit event
    s.events.Publish(ctx, events.StackDrifted{StackID: run.StackID, DriftRecordID: driftRecord.ID})

    // Handle remediation mode
    schedule, _ := s.repo.GetSchedule(ctx, run.StackID)
    return s.triggerRemediation(ctx, run.StackID, driftRecord.ID, schedule.DriftMode)
}

func (s *reconcileService) triggerRemediation(ctx context.Context, stackID uuid.UUID, driftID uuid.UUID, mode DriftMode) error {
    switch mode {
    case DriftModeNone:
        return nil // record only
    case DriftModeNotify:
        // Notification is handled by the event subscriber (Phase 6)
        return nil
    case DriftModeAutoPlan:
        run, err := s.runSvc.Create(ctx, run.CreateRunInput{
            OrgID:       stackID, // resolve org from stack
            StackID:     stackID,
            RunType:     run.RunTypePlan,
            TriggerType: run.TriggerTypeDrift,
        })
        if err != nil { return err }
        return s.repo.SetRemediationRun(ctx, driftID, run.ID)
    case DriftModeAutoApply:
        run, err := s.runSvc.Create(ctx, run.CreateRunInput{
            OrgID:       stackID,
            StackID:     stackID,
            RunType:     run.RunTypeApply,
            TriggerType: run.TriggerTypeDrift,
        })
        if err != nil { return err }
        return s.repo.SetRemediationRun(ctx, driftID, run.ID)
    }
    return nil
}
```

**Callback integration — where is `ProcessDriftResult` called?**

In `internal/run/service.go`, after a run event transitions a run to `APPLIED`, check if it's a drift-detect run:

```go
// internal/run/service.go — after transition to APPLIED

if run.RunType == RunTypeDriftDetect {
    planOutput := r.getPlanOutput(ctx, run.ID)
    go r.reconcileSvc.ProcessDriftResult(ctx, run.ID, planOutput)
    // Use goroutine to avoid blocking the transition; errors are logged
}
```

This is an intentional coupling point between Run and Reconcile contexts. It is the ONLY point where Run calls Reconcile. Run holds an optional `ReconcileService` interface reference (nil in Phase 2-4, set in Phase 5).

---

## Backoff on Consecutive Failures

```go
// internal/reconcile/repository.go

func (r *ReconcileRepository) RecordFailure(ctx context.Context, stackID uuid.UUID) error {
    _, err := r.db.Exec(ctx, `
        UPDATE reconcile_schedules
        SET consecutive_failures = consecutive_failures + 1,
            next_check_at = now() + (reconcile_interval * POWER(2, LEAST(consecutive_failures, 5)))
        WHERE stack_id = $1
    `, stackID)
    // POWER(2, failures): 2x, 4x, 8x, 16x, 32x — capped at 32x original interval
    // For 1h interval: 2h, 4h, 8h, 16h, 32h max
    return err
}
```

After 5 consecutive failures (consecutive_failures >= 5): the interval caps at 32x. Beyond 10 failures, the schedule is automatically disabled and an alert event is emitted.

---

## Drift Record Lifecycle

```
DETECTED    → drift found by drift-detect run
REMEDIATING → remediation run created (AUTO_PLAN or AUTO_APPLY mode)
RESOLVED    → subsequent apply run brought stack to desired state
IGNORED     → operator explicitly marked as ignored (not a problem)
```

**Resolution:** When a stack that has an open `DETECTED` or `REMEDIATING` drift record completes an `APPLIED` run (any type), check if the new state matches desired state. If the subsequent drift-detect run reports no changes, resolve the drift record.

Simplified approach for Phase 5: resolve drift record when any apply run succeeds for the stack.

```go
// Called from run service after APPLIED transition
if run.RunType == RunTypeApply {
    s.reconcileSvc.ResolveDrift(ctx, run.StackID)
}
```

```go
// internal/reconcile/service.go
func (s *reconcileService) ResolveDrift(ctx context.Context, stackID uuid.UUID) error {
    // Find open drift records for this stack
    records, _ := s.repo.ListOpenDriftRecords(ctx, stackID)
    for _, record := range records {
        s.repo.ResolveDriftRecord(ctx, record.ID)
    }
    s.stackSvc.SetStatus(ctx, stackID, stack.StatusActive)
    s.events.Publish(ctx, events.DriftResolved{StackID: stackID})
    return nil
}
```

---

## API Endpoints

```
GET    /api/v1/stacks/{stack_id}/reconcile         Get reconcile schedule config
PATCH  /api/v1/stacks/{stack_id}/reconcile         Update interval, drift_mode, enabled
POST   /api/v1/stacks/{stack_id}/reconcile/trigger  Manual drift-detect trigger
  Response: { run_id: "..." }

GET    /api/v1/stacks/{stack_id}/drift             List drift records for stack
GET    /api/v1/orgs/{org_id}/drift                 List all drift records for org (paginated, filterable by status)
GET    /api/v1/drift/{drift_id}                    Get drift record detail
POST   /api/v1/drift/{drift_id}/ignore             Mark drift as ignored
  Body: { reason: "..." }
```

---

## Reconcile Schedule Bootstrap

When a stack is created (Phase 1), a `reconcile_schedules` row should be inserted with defaults. Modify `internal/stack/service.go` Create method to also insert a reconcile schedule row within the same transaction.

```go
// internal/stack/service.go — Create method
// Within transaction, after inserting stack row:
_, err = tx.Exec(ctx, `
    INSERT INTO reconcile_schedules (stack_id, org_id, reconcile_interval, next_check_at)
    VALUES ($1, $2, '1 hour', now() + interval '1 hour')
`, stack.ID, stack.OrgID)
```

The first reconcile check is delayed by one interval to allow the stack to be configured fully before drift detection begins.

---

## Observability

New metrics added in Phase 5:
```
stratum_reconciler_stacks_checked_total          counter  — stacks processed per tick
stratum_reconciler_check_duration_seconds        histogram — time per stack check
stratum_drift_records_total{status}              gauge    — current drift record counts by status
stratum_drift_detected_total{org_id}             counter  — total drift detections
stratum_reconcile_failures_total{org_id}         counter  — consecutive failure increments
```

---

## Validation Criteria

After Phase 5:
1. Create a stack → reconcile schedule row exists with next_check_at = now() + 1h
2. Patch schedule to interval=1min, enabled=true → within 1 min, a drift-detect run is created automatically
3. Manually trigger reconcile via API → drift-detect run created immediately
4. Simulate drift: manually modify stack state file to show a resource change → drift-detect run detects it → drift record created → stack status = DRIFTED
5. With drift_mode=AUTO_PLAN: verify plan run is automatically created after drift detection
6. Complete an apply run on a drifted stack → drift record moves to RESOLVED → stack status = ACTIVE
7. Mark drift as ignored via API → drift record status = IGNORED → stack status returns to ACTIVE
8. Cause 6 consecutive drift-detect failures → next_check_at uses exponential backoff interval
