# Orchestration Model

## Scheduler

The Scheduler is a background goroutine in the control plane that drives the run lifecycle forward. It is NOT a separate service — it runs in-process with the API server in Phase 0-2, and can be extracted to a standalone process later.

**Responsibilities:**
- Polling the job queue for pending runs
- Dispatching runs to eligible worker pools
- Enforcing stack-level concurrency (one active run per stack)
- Enforcing DAG execution order across dependent stacks
- Timing out stale claims (worker died without reporting)

**Scheduler tick:** Runs on a configurable interval (default: 5s). On each tick:
1. Load all `PENDING` runs not blocked by dependencies
2. For each run, check if its stack has an active run — skip if locked
3. Check if policy evaluation is required pre-queue — if yes, evaluate inline
4. Write a job queue entry with `status=AVAILABLE`
5. A worker claims the job via `SELECT ... SKIP LOCKED FOR UPDATE`

---

## Job Queue (PostgreSQL-backed)

In Phase 0-2, the job queue is a PostgreSQL table. This is intentional — it avoids operational dependency on NATS or any external queue during early development.

```
Table: run_jobs
─────────────────────────────────────────────────
id           UUID        PK
run_id       UUID        FK → runs
pool_id      UUID        FK → worker_pools (nullable = any pool)
status       ENUM        AVAILABLE | CLAIMED | COMPLETED | FAILED
claimed_by   UUID        FK → workers (nullable)
claimed_at   TIMESTAMPTZ nullable
expires_at   TIMESTAMPTZ claim expiry (heartbeat-based)
attempt      INT         retry count
created_at   TIMESTAMPTZ
```

**Claim protocol:**
```sql
WITH claimed AS (
  SELECT id FROM run_jobs
  WHERE status = 'AVAILABLE'
    AND (pool_id = $worker_pool_id OR pool_id IS NULL)
    AND expires_at < now()
  ORDER BY created_at
  LIMIT 1
  FOR UPDATE SKIP LOCKED
)
UPDATE run_jobs
SET status = 'CLAIMED', claimed_by = $worker_id, claimed_at = now(), expires_at = now() + interval '60 seconds'
WHERE id = (SELECT id FROM claimed)
RETURNING *;
```

`SKIP LOCKED` is PostgreSQL's advisory for "skip rows locked by another transaction." This gives us a reliable work-stealing queue without a message broker. Throughput is adequate for hundreds of concurrent runs; NATS replaces this at scale.

**Heartbeat:** Workers send a heartbeat every 15s. The scheduler re-queues any job whose `expires_at` has passed and `status = CLAIMED`. This handles dead workers.

---

## Stack Dependency Graph (DAG)

Stacks can declare dependencies on other stacks:

```
Stack A (VPC)
    └── Stack B (EKS cluster)  depends_on: [A]
            └── Stack C (App)  depends_on: [B]
```

The DAG is stored as an adjacency list:

```
Table: stack_dependencies
─────────────────────────────────────
stack_id        UUID    the dependent
depends_on_id   UUID    the upstream
```

On each scheduler tick, the run-eligibility check performs a topological sort:

1. Load all `PENDING` runs
2. For each pending run, load its stack's upstream dependencies
3. Check if all upstream stacks have `APPLIED` state for the current desired configuration
4. If any upstream stack is `DRIFTED`, `LOCKED`, or has an active run — block the downstream run

**Cycle detection:** Enforced on write. When adding a dependency edge, the Stack service traverses the existing graph depth-first. If a cycle is detected, the write is rejected with a validation error.

**Fan-out execution:** When Stack A completes an apply, the scheduler immediately queues runs for all direct dependents (B) if they are pending.

---

## Run Triggering Sources

| Source | Mechanism | Run Type |
|--------|-----------|---------|
| VCS push to tracked branch | Webhook → VCS context → Stack context → Run context | `plan` or `apply` |
| Manual trigger via API | Direct API call | any |
| Scheduled (cron) | Scheduler checks `stack.schedule` expression | `plan` or `apply` |
| Drift detected | Reconciler → Run context | `drift-detect` → optionally `apply` |
| Upstream stack applied | Scheduler DAG fan-out | `plan` or `apply` |
| Approval granted | Approval gate in Run context | `apply` (continues existing run) |

---

## Concurrency Controls

**Stack-level lock:** At most one non-terminal run per stack. Enforced by:
1. Scheduler (pre-queue check)
2. Database unique constraint: `UNIQUE (stack_id) WHERE status NOT IN ('APPLIED', 'FAILED', 'CANCELLED', 'DISCARDED', 'POLICY_REJECTED')`

**State lock:** When a worker begins applying, it acquires a PostgreSQL advisory lock keyed on the stack ID. This prevents concurrent Terraform applies even in failure/retry edge cases.

**Pool capacity:** Each worker pool has a `max_concurrency` setting. The scheduler will not dispatch more runs to a pool than its capacity.

---

## NATS JetStream Integration (Phase 2+)

When NATS is introduced, the PostgreSQL job queue becomes an internal audit table (jobs are still recorded) but dispatch happens via NATS subjects:

```
Subject structure:
  stratum.runs.dispatch.{pool_id}     → worker receives run assignments
  stratum.runs.events.{run_id}        → run event fan-out (for WebSocket streaming)
  stratum.workers.heartbeat           → worker pool liveness
  stratum.stacks.events               → stack lifecycle events
  stratum.reconcile.trigger           → drift detection triggers
```

Workers subscribe to `stratum.runs.dispatch.{their_pool_id}`. The scheduler publishes to the pool's subject rather than using SKIP LOCKED. Both approaches are abstracted behind a `Dispatcher` interface — swapping implementations requires no scheduler logic changes.
