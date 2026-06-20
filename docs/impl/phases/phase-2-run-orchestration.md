# Phase 2: Run Orchestration

## Scope

**IN scope:**
- Run creation (all trigger types: manual, vcs_push, scheduled)
- Run state machine enforcement (transitions, validation)
- Run event store (append events, derive current state)
- Scheduler goroutine (PENDING → QUEUED, DAG ordering, concurrency enforcement)
- Job queue (PostgreSQL SKIP LOCKED)
- Approval gate (AWAITING_APPROVAL → APPLYING)
- Run cancellation
- Run list + detail API endpoints
- Run log ingestion and storage
- WebSocket log streaming (without NATS — in-memory pub/sub for Phase 2)
- Run event timeline endpoint
- VCS push → auto-run-creation (connects Phase 1 webhook to Phase 2 run creation)

**OUT of scope:**
- Actual worker execution (Phase 3) — runs will queue and stay QUEUED until Phase 3
- Policy evaluation (Phase 4) — policy gate is a stub returning PASS
- NATS integration (also Phase 2 stretch, but Phase 2 core uses in-memory event bus)

---

## Prerequisites

Phase 1 complete. Stacks, organizations, auth all working.

---

## Key Interface: RunService

```go
type RunService interface {
    Create(ctx context.Context, input CreateRunInput) (*Run, error)
    Get(ctx context.Context, id uuid.UUID) (*Run, error)
    List(ctx context.Context, filter RunFilter) ([]*Run, int, error)
    Cancel(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error
    Approve(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error
    Discard(ctx context.Context, id uuid.UUID, actorID uuid.UUID) error

    // Called by workers (Phase 3) — define interface now, implement in Phase 3
    AppendEvent(ctx context.Context, runID uuid.UUID, event RunEventInput) error
    GetTimeline(ctx context.Context, runID uuid.UUID) ([]*RunEvent, error)
    AppendLogs(ctx context.Context, runID uuid.UUID, lines []LogLine) error
    StreamLogs(ctx context.Context, runID uuid.UUID) (<-chan LogLine, error)
}
```

---

## Run State Machine Implementation

The state machine is implemented as a pure function — no side effects, just validation:

```go
// internal/run/statemachine.go

// ValidTransitions defines all legal state transitions
var ValidTransitions = map[RunState][]RunState{
    StatePending:          {StateQueued, StateCancelled},
    StateQueued:           {StateAssigned, StateCancelled},
    StateAssigned:         {StatePlanning, StateCancelled, StateQueued}, // re-queue on reassign
    StatePlanning:         {StatePlanned, StateFailed, StateCancelled},
    StatePlanned:          {StateAwaitingApproval, StateApplying, StatePolicyRejected, StateDiscarded, StateCancelled},
    StateAwaitingApproval: {StateApplying, StateDiscarded, StateCancelled},
    StateApplying:         {StateApplied, StateFailed, StateCancelled},
}

var TerminalStates = map[RunState]bool{
    StateApplied: true, StateFailed: true,
    StateCancelled: true, StateDiscarded: true, StatePolicyRejected: true,
}

func (sm *StateMachine) Transition(current RunState, next RunState) error {
    allowed := ValidTransitions[current]
    for _, s := range allowed {
        if s == next { return nil }
    }
    return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, current, next)
}
```

Every state transition is an atomic operation:
```
BEGIN;
  1. SELECT current_state FROM runs WHERE id=$id FOR UPDATE
  2. Validate transition via StateMachine.Transition()
  3. INSERT INTO run_events (run_id, seq, event_type, ...) VALUES (...)
  4. UPDATE runs SET current_state=$next, updated_at=now() WHERE id=$id
COMMIT;
```

The `seq` is assigned via:
```sql
SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = $run_id
```
(within the same transaction — safe because the FOR UPDATE lock on the run row serializes concurrent transitions)

---

## Scheduler Implementation

```go
// internal/run/scheduler.go

type Scheduler struct {
    runRepo    *RunRepository
    stackRepo  *stack.StackRepository   // for DAG queries
    clock      clock.Clock
    interval   time.Duration
    stopCh     chan struct{}
}

func (s *Scheduler) Start(ctx context.Context) {
    ticker := time.NewTicker(s.interval) // default: 5s
    for {
        select {
        case <-ticker.C:
            s.tick(ctx)
        case <-ctx.Done():
            return
        }
    }
}

func (s *Scheduler) tick(ctx context.Context) {
    // 1. Load all PENDING runs
    pendingRuns := s.runRepo.ListByState(ctx, StatePending)

    // 2. Load DAG and check eligibility
    for _, run := range pendingRuns {
        if s.isBlocked(ctx, run) { continue }
        if s.hasActiveRun(ctx, run.StackID) { continue }
        s.enqueue(ctx, run)
    }

    // 3. Detect timed-out claims and re-queue
    s.requeueTimedOutJobs(ctx)
}
```

**DAG blocking check:**
```go
func (s *Scheduler) isBlocked(ctx context.Context, run *Run) bool {
    upstreams := s.stackRepo.GetDependencies(ctx, run.StackID)
    for _, upstream := range upstreams {
        upstreamStack := s.stackRepo.GetByID(ctx, upstream.DependsOnID)
        // Block if upstream is DRIFTED, LOCKED, or has active run
        if upstreamStack.Status != "ACTIVE" { return true }
        if s.hasActiveRun(ctx, upstream.DependsOnID) { return true }
    }
    return false
}
```

---

## Job Queue — Claim Expiry and Re-queue

```go
func (s *Scheduler) requeueTimedOutJobs(ctx context.Context) {
    // Find jobs claimed but expired (worker died)
    timedOut := s.runRepo.ListTimedOutJobs(ctx)
    for _, job := range timedOut {
        run := s.runRepo.GetByID(ctx, job.RunID)
        if run.CurrentState == StateApplying {
            // Cannot safely re-queue mid-apply — mark failed
            s.transitionRun(ctx, run, StateFailed, "worker_timeout_during_apply")
        } else {
            // Safe to re-queue
            job.Attempt++
            if job.Attempt >= 3 {
                s.transitionRun(ctx, run, StateFailed, "max_attempts_exceeded")
            } else {
                s.runRepo.RequeueJob(ctx, job)
                s.transitionRun(ctx, run, StateQueued, "worker_timeout_requeue")
            }
        }
    }
}
```

---

## API Endpoints

```
POST   /api/v1/stacks/{stack_id}/runs         Create run (trigger_type=manual)
GET    /api/v1/stacks/{stack_id}/runs         List runs for stack (paginated)
GET    /api/v1/runs/{run_id}                  Get run detail
POST   /api/v1/runs/{run_id}/cancel           Cancel run
POST   /api/v1/runs/{run_id}/approve          Approve run (moves AWAITING → APPLYING)
POST   /api/v1/runs/{run_id}/discard          Discard run
GET    /api/v1/runs/{run_id}/timeline         Full event history
GET    /api/v1/runs/{run_id}/logs             Paginated log lines
WS     /api/v1/runs/{run_id}/logs/stream      Live log stream (WebSocket)
WS     /api/v1/runs/{run_id}/events/stream    Live event stream (WebSocket)

# Worker endpoints (stub in Phase 2, used in Phase 3)
POST   /api/v1/internal/workers/register
GET    /api/v1/internal/workers/{id}/jobs
POST   /api/v1/internal/workers/{id}/heartbeat
POST   /api/v1/internal/runs/{id}/events
POST   /api/v1/internal/runs/{id}/logs
GET    /api/v1/internal/runs/{id}/source-archive
POST   /api/v1/internal/runs/{id}/secrets/claim
```

Note the `/internal/` prefix for worker-facing endpoints — different auth middleware (worker token vs user JWT/API key).

---

## In-Memory WebSocket Hub (Phase 2)

Before NATS, WebSocket subscriptions use an in-memory pub/sub hub:

```go
// internal/api/ws/hub.go
type Hub struct {
    mu          sync.RWMutex
    subscribers map[string][]chan []byte  // key = run_id
}

func (h *Hub) Subscribe(runID string) <-chan []byte { ... }
func (h *Hub) Publish(runID string, msg []byte) { ... }
func (h *Hub) Unsubscribe(runID string, ch <-chan []byte) { ... }
```

When a run event is appended (via `AppendEvent`), the service also calls `hub.Publish(runID, serializedEvent)`. All WebSocket connections for that run_id receive the event.

**Limitation:** This hub is per-process. In a multi-instance deployment, events from one API instance won't reach WebSocket clients connected to another. NATS in Phase 3+ solves this.

---

## Log Storage

```
Table: run_logs
────────────────────────────────────────────────────────
id          UUID
run_id      UUID        FK → runs
seq         BIGINT      monotonic per-run
line        TEXT        one log line (no ANSI escape codes)
source      VARCHAR     stdout | stderr
occurred_at TIMESTAMPTZ
```

Logs are queried with pagination: `SELECT * FROM run_logs WHERE run_id = $id ORDER BY seq LIMIT 100 OFFSET $offset`

Log chunks from workers are batched (max 50 lines per POST). The seq counter is managed the same way as run_events seq.

---

## Validation Criteria

After Phase 2:
1. POST to create a run → run appears in PENDING state
2. Within 5s (scheduler tick), run moves to QUEUED (no worker exists yet — that's expected)
3. Run timeline shows `run.created` and `run.queued` events
4. Cancel a QUEUED run → run moves to CANCELLED; further queue processing is skipped
5. Create two stacks: A ← B (B depends on A). Queue a run for B. Verify B stays PENDING until a run for A is APPLIED. (Simulate A applied by directly updating stack status + creating a completed run record.)
6. WebSocket connect to `/runs/{id}/events/stream` → create a run → verify events arrive over WebSocket
