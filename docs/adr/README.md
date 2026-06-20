# Architecture Decision Records

ADRs document significant architectural decisions: what was decided, why, and what alternatives were considered. They are written once and rarely changed (amendments create new ADRs).

## Index

| # | Title | Status |
|---|-------|--------|
| 001 | Modular Monolith as Initial Architecture | Accepted |
| 002 | PostgreSQL Job Queue over Message Broker in Phase 0-2 | Accepted |
| 003 | Event Sourcing Scoped to Run Lifecycle Only | Accepted |
| 004 | OPA Embedded In-Process over OPA Server | Accepted |
| 005 | OpenTofu as Default IaC Executor | Accepted |
| 006 | Docker-in-Worker Execution Isolation | Accepted |
| 007 | Bounded Context Package Structure in Go | Accepted |
| 008 | NATS JetStream Deferred to Phase 2 | Accepted |
| 009 | Temporal Deferred to Phase 3+ | Accepted |
| 010 | PostgreSQL Advisory Locks for Scheduler Leader Election | Accepted |

---

## ADR-001: Modular Monolith as Initial Architecture

**Status:** Accepted
**Date:** 2024-01

### Context
The system requires multiple concerns: API serving, job scheduling, reconciliation, event fan-out, worker management. The temptation is to model these as separate microservices immediately.

### Decision
Start with a single deployable binary (`stratum-server`) containing all control plane logic. Bounded contexts are implemented as Go packages with explicit interfaces — not separate services.

### Consequences
- **Good:** Single process to deploy, single database, no distributed systems overhead, fast iteration, trivial local development
- **Good:** Bounded contexts are still cleanly separated; extraction to services is possible later without major refactoring
- **Bad:** Cannot scale individual components independently until extraction
- **Bad:** A crash of one component (e.g., reconciler panic) affects the entire process

### Alternatives Considered
- **Microservices from day one:** Rejected. Premature operational complexity. Would require service discovery, distributed tracing across many services, and network retry logic before any features exist.
- **Serverless functions:** Rejected. Poor fit for long-running operations (runs can take minutes), streaming (WebSocket log tailing), and background loops (reconciler).

---

## ADR-002: PostgreSQL Job Queue in Phase 0-2

**Status:** Accepted
**Date:** 2024-01

### Context
Workers need to claim run jobs. Multiple workers must not claim the same job. A reliable queue mechanism is required.

### Decision
Use a PostgreSQL table with `SELECT ... FOR UPDATE SKIP LOCKED` as the job queue. No external message broker in Phase 0-2.

### Consequences
- **Good:** Zero additional infrastructure. One system to operate.
- **Good:** ACID guarantees — job claims are transactional with other state updates
- **Good:** Full visibility into queue state via SQL queries
- **Bad:** PostgreSQL is not optimized for queue workloads at very high throughput (>1000 jobs/sec is challenging)
- **Bad:** Long-polling workers create persistent DB connections

### Migration Path
The scheduler and worker claim logic is behind a `Dispatcher` interface. In Phase 2, the implementation swaps to NATS JetStream publish/subscribe. The PostgreSQL queue table is retained as an audit record.

---

## ADR-003: Event Sourcing Scoped to Run Lifecycle

**Status:** Accepted
**Date:** 2024-01

### Context
Full event sourcing (all domain state derived from events) is architecturally elegant but operationally complex: CQRS read models, event schema evolution, projection rebuilding, and snapshot management.

### Decision
Apply event sourcing only to the Run lifecycle. All other contexts (Stack, IAM, Policy, etc.) use standard CRUD with an audit log.

### Consequences
- **Good:** Run history is complete and replayable — the primary audit requirement
- **Good:** No CQRS complexity for stack/policy/IAM (those entities change infrequently)
- **Good:** The `run_events` table is the only event store; it is simpler to maintain and optimize
- **Bad:** Cannot replay the full system state from events alone (only run state)
- **Bad:** Stack configuration history requires a separate versioning strategy (use `stack_config_versions` table)

---

## ADR-004: OPA Embedded In-Process

**Status:** Accepted
**Date:** 2024-01

### Context
Policy evaluation must happen before run execution. Options: embedded OPA SDK, standalone OPA server, custom policy DSL.

### Decision
Use the OPA Go SDK (`github.com/open-policy-agent/opa`) embedded in the control plane process. Policies are loaded from the database at startup and on policy update events.

### Consequences
- **Good:** Zero network latency for policy evaluation
- **Good:** No additional service to deploy or monitor
- **Good:** Policy bundles are database-versioned and hot-reloadable
- **Bad:** OPA evaluation is CPU-bound; heavy policy evaluation may compete with API serving (mitigated with goroutine pool)
- **Bad:** Cannot share policies across multiple control plane instances without re-loading from DB (acceptable — DB is the source of truth)

---

## ADR-009: Temporal Deferred to Phase 3+

**Status:** Accepted
**Date:** 2024-01

### Context
Temporal is architecturally ideal for durable run workflows. It handles retries, timeouts, activity failures, and long-running processes with code-as-workflow semantics.

### Decision
Do not use Temporal in Phase 0-2. Implement run orchestration using the PostgreSQL job queue and the scheduler. Define the `WorkflowEngine` interface such that Temporal can be added as an alternative implementation.

### Consequences
- **Good:** Phase 0-2 has no dependency on running a Temporal cluster (significant operational overhead)
- **Good:** The core run lifecycle logic is understandable without Temporal knowledge
- **Bad:** The PostgreSQL-based implementation is less robust than Temporal for edge cases (worker crashes mid-workflow, multi-step workflow coordination)
- **Bad:** Adding Temporal later requires careful migration of in-flight runs

### When to Revisit
When the PostgreSQL-based scheduler shows observable reliability issues with complex multi-stack DAG workflows, or when the development team has capacity to operate a Temporal cluster.
