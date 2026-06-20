# Event Model

## Philosophy

Stratum uses event sourcing for run state — not for the entire system. This is a deliberate scope decision.

**What is event-sourced:** Run lifecycle. Every transition is an event. State is derived.
**What is NOT event-sourced:** Stack configuration, IAM, policy definitions. These use standard CRUD with an audit log.

This scope avoids the operational complexity of full event sourcing (no event replay for configuration changes, no CQRS for reads) while gaining the primary benefits: complete run history, replay, audit trail.

---

## Event Store

```
Table: run_events
──────────────────────────────────────────────────────────────────
id            UUID          PK
run_id        UUID          FK → runs (indexed)
org_id        UUID          for multi-tenant partitioning (indexed)
seq           BIGINT        monotonic per-run sequence number
event_type    VARCHAR(64)   namespaced: "run.created", "policy.evaluated", etc.
actor_id      UUID          nullable — user or worker that caused the event
actor_type    ENUM          USER | WORKER | SYSTEM
payload       JSONB         event-specific data
occurred_at   TIMESTAMPTZ   set by the emitter (not DB default — allows worker timestamps)
inserted_at   TIMESTAMPTZ   DEFAULT now() — for ordering guarantees
```

**Ordering guarantee:** `(run_id, seq)` is the canonical ordering. `inserted_at` is secondary for cross-run ordering. Sequence numbers are assigned by the control plane at insert time using `nextval('run_event_seq_{run_id}')`. Workers report events with their local timestamp in `occurred_at` but the sequence is authoritative.

---

## Event Type Registry

Events are namespaced by context and named with dot notation:

```
Run lifecycle:
  run.created             run was created
  run.queued              added to job queue
  run.assigned            claimed by a worker
  run.planning_started    worker began terraform plan
  run.planned             plan complete, changes computed
  run.applying_started    worker began terraform apply
  run.applied             apply complete, state updated
  run.failed              terminal failure
  run.cancelled           explicit cancellation
  run.discarded           plan-only run closed without action

Policy:
  policy.evaluated        OPA evaluation completed
  policy.approved         manual approval received
  policy.timed_out        approval window expired

Worker:
  worker.heartbeat_missed worker failed to heartbeat
  worker.crashed          worker process died unexpectedly

Drift:
  drift.detected          reconciler found deviation
  drift.resolved          subsequent apply matched desired state

Log:
  log.chunk               batch of log lines from worker (streamed, not stored in events table)
```

---

## Materialised Run State View

Rather than deriving state on every read (event replay), a PostgreSQL view materialises the current state:

```sql
CREATE MATERIALIZED VIEW run_current_state AS
SELECT DISTINCT ON (run_id)
  run_id,
  event_type AS current_event,
  occurred_at AS state_changed_at,
  payload
FROM run_events
ORDER BY run_id, seq DESC;
```

Refreshed via `REFRESH MATERIALIZED VIEW CONCURRENTLY` on each new run event insertion (via trigger). For high-throughput scenarios, a `runs` table with a `current_state` column updated via trigger is faster — this is the Phase 3 optimisation.

---

## Outbox Pattern

To guarantee that events are both written to the database AND published to NATS without dual-write failure risk, Stratum uses the Transactional Outbox pattern:

```
Within a single PostgreSQL transaction:
  1. Write run_events row
  2. Write outbox_messages row (event payload, destination subject, status=PENDING)

Outbox relay (background goroutine, 1s tick):
  3. SELECT * FROM outbox_messages WHERE status='PENDING' FOR UPDATE SKIP LOCKED
  4. Publish to NATS JetStream
  5. Mark status=DELIVERED
```

If the NATS publish fails, the outbox entry remains PENDING and is retried. If the control plane crashes after DB write but before NATS publish, the relay publishes on restart.

This guarantees at-least-once delivery to NATS consumers. Consumers must be idempotent (use event ID for deduplication).

```
Table: outbox_messages
──────────────────────────────────────────────────
id            UUID
subject       VARCHAR     NATS subject
payload       JSONB       event data
status        ENUM        PENDING | DELIVERED | FAILED
attempt       INT
created_at    TIMESTAMPTZ
delivered_at  TIMESTAMPTZ nullable
```

---

## NATS JetStream Subjects and Consumers

```
Stream: STRATUM_RUNS
  Subjects: stratum.runs.events.>
  Retention: WorkQueuePolicy (for worker dispatch)
             InterestPolicy (for WebSocket fan-out)
  Max age: 30 days

Stream: STRATUM_STACKS
  Subjects: stratum.stacks.events.>
  Retention: LimitsPolicy
  Max age: 90 days

Consumers:
  websocket-broadcaster     pushes run events to connected UI clients
  audit-archiver            writes to long-term audit table
  reconcile-trigger         starts drift detection on stack.applied events
  notification-router       sends Slack/email on run.failed, run.applied
```

---

## Audit Log

The audit log is a separate table from the event store. It captures user actions across ALL contexts (not just runs):

```
Table: audit_log
────────────────────────────────────────────────────────
id            UUID
org_id        UUID
actor_id      UUID        user or API key ID
actor_type    ENUM        USER | API_KEY | SYSTEM
action        VARCHAR     "stack.created", "policy.updated", "run.cancelled", ...
resource_type VARCHAR     "stack", "run", "policy", ...
resource_id   UUID
old_value     JSONB       nullable — for update actions
new_value     JSONB       nullable
ip_address    INET        for user actions
occurred_at   TIMESTAMPTZ
```

The audit log is append-only. Rows are never updated or deleted. This is a compliance requirement.

---

## WebSocket Realtime Streaming

The UI subscribes to run events via WebSocket:

```
WS: /api/v1/runs/{run_id}/stream

Server:
  1. Sends all historical events for run_id on connect (catch-up)
  2. Subscribes to NATS subject: stratum.runs.events.{run_id}
  3. Forwards each NATS message to the WebSocket client as JSON
  4. Closes WebSocket when run reaches terminal state

Log streaming:
  Separate WebSocket endpoint: /api/v1/runs/{run_id}/logs/stream
  Workers POST log chunks to the API; API fans out to WebSocket subscribers
  Log chunks are stored in a separate, high-volume table (not the event store)
```

---

## Event Replay

For a given run, an operator can request a replay of its event sequence. This produces:
- A complete timeline of every state transition
- All policy evaluations with inputs and verdicts
- All log output in chronological order
- Worker assignment history
- Duration at each state

This is exposed via: `GET /api/v1/runs/{run_id}/timeline`

Full infrastructure replay (re-executing a previous desired-state configuration) is a Phase 5 feature — it creates a new run seeded from the historical run's configuration snapshot.
