# Scaling and Failure Handling

## Scaling Philosophy

Stratum starts as a modular monolith. The scaling strategy is:

```
Phase 0-2:  Single process, single PostgreSQL, no external queue
Phase 3:    NATS JetStream added; control plane horizontally scalable (stateless API)
Phase 4:    Workers scale independently; hosted worker pool on Kubernetes
Phase 5+:   Scheduler/Reconciler extracted to standalone processes if warranted
```

**Do not prematurely extract services.** The scheduler and reconciler are goroutines inside the API server binary in Phase 0-3. They become independent binaries only when horizontal scaling of the control plane creates leader-election contention.

---

## Control Plane Scaling

### API Server (Stateless)
The API server has no in-process state after Phase 2 (all state in PostgreSQL + NATS). Horizontal scaling is trivially: run N instances behind a load balancer.

**Session state:** JWTs are stateless. Refresh tokens are stored in PostgreSQL — any API instance can validate them. No sticky sessions required.

**WebSocket scaling:** Each WebSocket connection (run log streaming) is pinned to one API instance. In a multi-instance deployment, a NATS subscriber per-instance handles fan-out. NATS delivers the event to all subscribers, each forwarding to their connected clients. No cross-instance coordination needed.

### Scheduler (Leader-elected)
The scheduler is a single logical process (to avoid double-queuing). In a multi-instance API deployment, the scheduler uses PostgreSQL advisory locks for leader election:

```
Every scheduler instance:
  1. Attempt to acquire advisory lock: pg_try_advisory_lock(SCHEDULER_LOCK_ID)
  2. If acquired: become leader, run scheduler loop
  3. If not acquired: become standby, retry lock acquisition every 30s
  4. Leader releases lock on graceful shutdown
  5. Standby acquires lock within 30s of leader crash
```

This is operationally simple. For throughput beyond ~10,000 concurrent runs, a distributed scheduler (like the one in Temporal) is warranted — but that's not a near-term concern.

### Reconciler (Leader-elected)
Same pattern as the scheduler. One reconciler leader. Standbys are ready to take over.

---

## PostgreSQL Scaling

### Phase 0-3 (Single Primary)
A single PostgreSQL primary handles all workloads. Connection pooling via PgBouncer (transaction-mode pooling). The schema is designed to avoid hot-spot contention:
- Job queue uses SKIP LOCKED — no row contention
- Event inserts are append-only — no update contention
- State locks use advisory locks — not row locks on production tables

### Phase 4+ (Read Replicas)
Add PostgreSQL streaming replicas for read-heavy queries:
- Run history/timeline queries → replica
- Audit log queries → replica
- All writes, job queue, advisory locks → primary

### Phase 5+ (Partitioning)
The `run_events` and `audit_log` tables are the highest-volume tables. Partition by `org_id` hash (8 partitions) for parallel scan performance. Add range partitioning on `inserted_at` for time-based queries.

---

## Worker Scaling

Workers are independently scalable. Hosted worker scaling:

**Phase 0-2:** Fixed worker pool size, configured at startup.
**Phase 3:** Auto-scaling based on job queue depth:
```
Queue depth > 10 pending jobs AND pool at capacity → spawn additional workers (up to max_size)
Queue depth = 0 AND idle workers > min_size → terminate idle workers (scale-in delay: 5m)
```

Private workers are customer-managed and scale independently.

---

## Failure Handling Taxonomy

### Transient Failures (retry safe)
- Network timeouts between worker and control plane
- PostgreSQL deadlock (rare, automatically retried by application)
- NATS publish failure (handled by outbox relay retry)
- Policy service temporary unavailability

**Strategy:** Exponential backoff with jitter, max 3 attempts, then fail with error event.

### Worker Failures (mid-execution)
- Worker process crash during PLANNING: → re-queue, new worker picks up (safe, no infra changes)
- Worker process crash during APPLYING: → mark FAILED, create drift record, require manual review
- Worker heartbeat timeout: → same as crash handling based on current state

**Why is mid-apply crash not retried?**
Terraform apply is not idempotent in all cases. Some providers create resources before applying all changes. A re-apply may duplicate resources or fail due to partially-created state. Human review is required.

### Control Plane Failures
- API server crash: load balancer routes to other instances; in-flight requests fail with 502 (client retries)
- Scheduler crash: leader election promotes standby within 30s; no runs are lost (they remain QUEUED in DB)
- PostgreSQL primary failure: promotion of replica required (manual or via pg_auto_failover); typical RTO: 30-60s
- NATS failure: outbox entries accumulate; delivered in order when NATS recovers; WebSocket clients reconnect and replay from event store

### Data Failures
- Partial event insertion: events are inserted in a transaction with the run state update; partial writes are rolled back
- Duplicate event delivery (NATS at-least-once): consumers deduplicate by event ID
- State file corruption: state versions are retained; operator can roll back to previous version

---

## Observability Model

### Metrics (Prometheus via OpenTelemetry)

Key metrics exposed at `/metrics`:
```
stratum_runs_total{org_id, status, type}          counter
stratum_runs_duration_seconds{type, status}        histogram
stratum_queue_depth{pool_id}                       gauge
stratum_workers_active{pool_id}                    gauge
stratum_drift_detected_total{org_id}              counter
stratum_policy_evaluations_total{verdict}         counter
stratum_scheduler_tick_duration_seconds           histogram
stratum_reconciler_stacks_checked_total           counter
```

### Traces (OpenTelemetry → Jaeger/Tempo)
Trace context propagated through:
- HTTP request → scheduler dispatch → worker job claim → execution → event ingestion

Key spans:
```
http.request                       API endpoint
run.scheduler.enqueue              scheduler to queue
worker.job.claim                   worker claim
worker.execution.plan              container execution
worker.execution.apply             container execution
policy.evaluation                  OPA evaluation
state.lock.acquire                 advisory lock
```

### Logs (Structured JSON → Loki)
All log lines are structured JSON. Standard fields:
```json
{
  "level": "info",
  "ts": "2024-01-01T00:00:00Z",
  "caller": "run/scheduler.go:142",
  "msg": "run queued",
  "run_id": "r-xxx",
  "stack_id": "s-xxx",
  "org_id": "o-xxx",
  "trace_id": "xxx"
}
```

No `fmt.Println` or unstructured logging anywhere in the codebase.

---

## SLO Targets (Phase 3+)

| Metric | Target |
|--------|--------|
| API availability | 99.9% |
| Run queue latency (PENDING → QUEUED) | p95 < 10s |
| Run dispatch latency (QUEUED → ASSIGNED) | p95 < 30s |
| Drift detection interval accuracy | ±5m of configured interval |
| Policy evaluation latency | p99 < 500ms |
| WebSocket event delivery latency | p95 < 2s |
