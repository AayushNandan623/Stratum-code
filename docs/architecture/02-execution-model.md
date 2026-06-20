# Execution Model

## The Run

A **Run** is the fundamental unit of execution in Stratum. It represents a single attempt to reconcile a stack's actual state toward its desired state.

Every run has a **type**:
- `plan` — Produces a diff between desired and actual state. No changes are applied.
- `apply` — Applies planned changes to infrastructure. Requires an approved plan.
- `destroy` — Removes all infrastructure managed by this stack.
- `drift-detect` — Compares actual state to desired state without applying. Triggered by the Reconciler.

---

## Run State Machine

```
                   ┌─────────┐
              ┌───▶│ PENDING │
              │    └────┬────┘
              │         │ scheduler picks up
              │         ▼
              │    ┌─────────┐
              │    │ QUEUED  │◀─── (re-queued on worker timeout)
              │    └────┬────┘
              │         │ worker claims
              │         ▼
              │    ┌──────────┐
              │    │ ASSIGNED │
              │    └────┬─────┘
              │         │ worker starts
              │         ▼
              │    ┌──────────────┐
              │    │   PLANNING   │
              │    └──────┬───────┘
              │           │
              │    ┌──────┴────────────┐
              │    │                   │
              │    ▼                   ▼
              │ ┌────────┐        ┌─────────┐
              │ │ FAILED │        │ PLANNED │
              │ └────────┘        └────┬────┘
              │                        │
              │         ┌──────────────┼──────────────┐
              │         ▼              ▼              ▼
              │    ┌──────────┐  ┌──────────┐  ┌───────────┐
              │    │ AWAITING │  │POLICY    │  │ DISCARDED │
              │    │APPROVAL  │  │REJECTED  │  │(plan-only)│
              │    └────┬─────┘  └──────────┘  └───────────┘
              │         │ approved
              │         ▼
              │    ┌──────────┐
              │    │ APPLYING │
              │    └────┬─────┘
              │         │
              │    ┌─────┴──────┐
              │    ▼            ▼
              │ ┌────────┐  ┌─────────┐
              │ │ FAILED │  │ APPLIED │
              │ └────────┘  └─────────┘
              │
              │ CANCELLED is reachable from any non-terminal state
              └─────────────────────────────────────────────────
```

### Terminal States
- `APPLIED` — Run completed successfully. Infrastructure matches desired state.
- `FAILED` — Run encountered an error during planning or applying.
- `DISCARDED` — A plan-only run whose output was reviewed but not actioned.
- `CANCELLED` — Run was explicitly cancelled by a user or the system.
- `POLICY_REJECTED` — Policy evaluation produced a hard-fail verdict.

### Key Transitions

| From | To | Trigger | Notes |
|------|-----|---------|-------|
| `PENDING` | `QUEUED` | Scheduler picks up | Happens within scheduler tick interval |
| `QUEUED` | `ASSIGNED` | Worker claims the run | Worker must heartbeat within claim timeout |
| `ASSIGNED` | `PLANNING` | Worker reports planning started | |
| `PLANNING` | `PLANNED` | Worker reports plan complete | |
| `PLANNING` | `FAILED` | Worker reports error | |
| `PLANNED` | `AWAITING_APPROVAL` | Stack requires approval gate | |
| `PLANNED` | `APPLYING` | Auto-apply enabled, policy passed | |
| `PLANNED` | `POLICY_REJECTED` | OPA hard-fail | |
| `PLANNED` | `DISCARDED` | Plan-only run type | |
| `AWAITING_APPROVAL` | `APPLYING` | Approval received | |
| `AWAITING_APPROVAL` | `DISCARDED` | Approval timeout or explicit discard | |
| `APPLYING` | `APPLIED` | Worker reports apply complete | |
| `APPLYING` | `FAILED` | Worker reports apply error | |
| Any non-terminal | `CANCELLED` | User or system cancellation | Propagated to worker via cancellation channel |

---

## Run as an Event Stream

A run's state is **derived** from its event log, not stored directly. The events table for a run looks like:

```
run_id | seq | event_type          | payload            | occurred_at
-------|-----|---------------------|--------------------|------------
r-001  | 1   | run.created         | {type: "apply"}    | 2024-01-01T00:00:00Z
r-001  | 2   | run.queued          | {queue_id: "q-1"}  | 2024-01-01T00:00:01Z
r-001  | 3   | run.assigned        | {worker_id: "w-1"} | 2024-01-01T00:00:05Z
r-001  | 4   | run.planning_started| {}                 | 2024-01-01T00:00:06Z
r-001  | 5   | run.planned         | {changes: 3}       | 2024-01-01T00:01:00Z
r-001  | 6   | policy.evaluated    | {verdict: "pass"}  | 2024-01-01T00:01:01Z
r-001  | 7   | run.applying_started| {}                 | 2024-01-01T00:01:02Z
r-001  | 8   | run.applied         | {duration_s: 47}   | 2024-01-01T00:01:49Z
```

**Current state** = the event_type of the last event, mapped to a state enum.
**Materialised view** = PostgreSQL view that reads the max-seq event per run for fast queries.

This model provides: full audit history, replay capability, and zero risk of state inconsistency (events are only appended, never updated).

---

## Stack State Machine

Stacks have a separate lifecycle:

```
┌────────┐   stack created   ┌────────┐
│  INIT  │──────────────────▶│ ACTIVE │◀──────────────────┐
└────────┘                   └───┬────┘                   │
                                 │                        │
              ┌──────────────────┼──────────────┐         │
              ▼                  ▼              ▼         │
         ┌─────────┐      ┌──────────┐   ┌──────────┐     │
         │ DRIFTED │      │  LOCKED  │   │DESTROYING│     │
         └────┬────┘      └──────────┘   └────┬─────┘     │
              │                               │           │
              │ remediation run applied       ▼           │
              └──────────────────────▶ ┌──────────┐      │
                                       │DESTROYED │      │
                                       └──────────┘      │
                                                         │
              reconciliation applied ────────────────────┘
```

---

## Idempotency Semantics

**Run creation is idempotent.** A client can retry run creation with the same `idempotency_key` and receive the existing run rather than creating a duplicate.

**State transitions are idempotent.** If a worker sends `run.applied` twice (network retry), the second event is a no-op if the run is already in `APPLIED`.

**Plan outputs are deterministic.** The same stack configuration + state = the same plan output. Workers cache plan output by content hash.

---

## Retry Semantics

| Failure Scenario | Behavior |
|-----------------|----------|
| Worker crashes mid-planning | Run moves to `QUEUED` after claim timeout. New worker picks up. |
| Worker crashes mid-applying | Run moves to `FAILED`. Manual review required (infrastructure may be partially applied). |
| Policy service unavailable | Run stays in `PLANNED`, retried with exponential backoff. Not auto-failed. |
| Apply fails with Terraform error | Run moves to `FAILED`. Error logged. Remediation run can be triggered. |

**Why is mid-apply crash treated differently?**
Apply is NOT safely retryable without human review. Partial infrastructure changes may exist. The system marks the run `FAILED` and creates a drift record. A human must review before a new apply is triggered.
