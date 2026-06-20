# Reconciliation Model

## The Desired-State Philosophy

Every stack has a **desired state** (IaC configuration + variable values) and an **actual state** (the last known infrastructure state, recorded in a Terraform state file). Drift is when these diverge without a run having been executed.

Stratum's reconciler is a continuous background controller — conceptually identical to a Kubernetes controller — that watches for drift and drives the system toward convergence.

---

## Reconciler Architecture

The Reconciler is a goroutine pool inside the control plane. It is NOT a separate service. It operates on a per-stack schedule.

```
Reconciler Loop (per stack):
─────────────────────────────────────────────────
1. Every stack has a reconcile_interval (default: 1h, min: 15m, max: 24h)
2. Reconciler maintains a priority queue of stacks sorted by next_check_at
3. On each tick, pop all stacks with next_check_at <= now()
4. For each stack: dispatch a drift-detect job
5. Set next_check_at = now() + reconcile_interval
```

**Priority queue implementation:**
PostgreSQL table with index on `next_check_at`. The reconciler uses `SELECT ... FOR UPDATE SKIP LOCKED` to claim stacks for checking — the same pattern as the job queue. This allows multiple reconciler goroutines to work in parallel without coordination overhead.

```
Table: reconcile_schedule
─────────────────────────────────────────────────────
stack_id          UUID        FK → stacks
next_check_at     TIMESTAMPTZ indexed
last_check_at     TIMESTAMPTZ nullable
last_drift_at     TIMESTAMPTZ nullable — when drift was last detected
reconcile_interval INTERVAL   default '1 hour'
enabled           BOOLEAN     default true
```

---

## Drift Detection Execution

A drift-detect run executes `tofu plan -refresh-only` (or equivalent). It does NOT apply anything. The output is compared against the last known state.

```
Drift detection flow:
  1. Reconciler creates a run with type=DRIFT_DETECT
  2. Run is queued and dispatched to a worker (same path as normal runs)
  3. Worker runs: tofu plan -refresh-only -json
  4. Worker parses plan JSON output:
       - resource_changes with action != "no-op" → drift detected
       - empty resource_changes → no drift
  5. Worker reports plan output to control plane
  6. Control plane evaluates drift:
       - No changes: update last_check_at, status=CLEAN
       - Changes detected: write drift record, emit stack.drifted event
```

---

## Drift Record

```
Table: drift_records
─────────────────────────────────────────────────────
id                UUID
stack_id          UUID
org_id            UUID
run_id            UUID          the drift-detect run that found it
detected_at       TIMESTAMPTZ
resolved_at       TIMESTAMPTZ   nullable — set when remediation run succeeds
status            ENUM          DETECTED | REMEDIATING | RESOLVED | IGNORED
resource_count    INT           number of drifted resources
drift_summary     JSONB         per-resource change type (add/remove/update)
remediation_run_id UUID         nullable — the apply run triggered to fix it
```

**Drift summary structure:**
```json
{
  "added": ["aws_instance.bastion"],
  "removed": [],
  "updated": ["aws_security_group.main", "aws_route53_record.api"]
}
```

---

## Remediation Modes

Stacks configure their drift remediation mode:

| Mode | Behavior |
|------|----------|
| `NONE` | Detect drift and record it. No automatic action. |
| `NOTIFY` | Detect drift, record it, send notification. No automatic apply. |
| `AUTO_PLAN` | Detect drift, queue a `plan` run automatically. Requires human approval to apply. |
| `AUTO_APPLY` | Detect drift, queue an `apply` run automatically. No approval required. |

`AUTO_APPLY` should only be used for non-production stacks. The default is `NOTIFY`.

---

## Reconciler as a Kubernetes Controller Analogue

The Stratum Reconciler intentionally mirrors the Kubernetes controller pattern:

| Kubernetes | Stratum |
|------------|---------|
| Desired state in etcd (spec) | Desired state = IaC config + variable values |
| Actual state = running pods/resources | Actual state = Terraform state file |
| Controller watch loop | Reconciler schedule loop |
| Informer cache | Reconcile schedule table with next_check_at |
| Reconcile function | Drift-detect run |
| RequeueAfter | next_check_at = now() + interval |
| Status conditions | drift_records table |

The key difference: Kubernetes controllers reconcile in seconds. Infrastructure reconciliation is expensive (Terraform refresh = real API calls to AWS/GCP/etc), so the minimum interval is 15 minutes.

---

## Event-Driven Reconciliation Triggers

The schedule-based loop is supplemented by event-driven triggers:

| Event | Trigger |
|-------|---------|
| `run.applied` | Reset drift status for the stack to CLEAN |
| `stack.config_updated` | Immediately queue a drift-detect (config changed — state may now differ) |
| `stack.vcs_push` | Queue a plan run (desired state may have changed) |
| Manual via API | `POST /api/v1/stacks/{id}/reconcile` |

---

## Reconciler Failure Handling

**If drift-detect run fails:**
- Increment `reconcile_failures` counter on schedule record
- Apply exponential backoff: next_check_at = now() + min(interval * 2^failures, 24h)
- After 5 consecutive failures: disable reconciliation for this stack, emit alert event

**If remediation run fails:**
- Drift record status stays REMEDIATING
- Emit `drift.remediation_failed` event
- Switch stack to manual intervention mode — no further auto-remediation attempts
- Operator must review and re-enable

---

## Stack State Transitions Driven by Reconciler

```
ACTIVE  ──[drift detected]──▶  DRIFTED
DRIFTED ──[remediation applied]──▶  ACTIVE
DRIFTED ──[operator marked ignored]──▶  ACTIVE (drift_record.status = IGNORED)
```

A `DRIFTED` stack's runs are not blocked — a human can still queue a plan/apply. The drift status is informational for `NOTIFY` mode and blocks new runs only for stacks configured with `require_clean_state_to_run = true`.
