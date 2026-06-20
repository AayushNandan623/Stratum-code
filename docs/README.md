# Stratum — Infrastructure Control Plane
## Documentation Master Index

**Stratum** is a cloud-native infrastructure orchestration control plane. It manages the full lifecycle of infrastructure: stacks, runs, workflows, drift detection, policy enforcement, and execution isolation.

---

## Two Audiences, Two Documentation Trees

```
docs/
├── architecture/    → Human developer: deep understanding, reasoning, tradeoffs
├── adr/             → Architecture Decision Records: why decisions were made
├── impl/            → AI coding agents: lean, isolated, implementation-focused
└── ops/             → Operational: deployment, runbooks, local dev
```

**If you are a human developer** → start with `architecture/00-system-overview.md`, then follow the learning roadmap below.

**If you are an AI coding agent** → go directly to `impl/README.md`, then the specific phase/module document for your task. Do NOT load architecture docs unless required for your specific task.

---

## Human Learning Roadmap

Read in this order for full system understanding:

| Step | File | What You Learn |
|------|------|----------------|
| 1 | `architecture/00-system-overview.md` | What Stratum is, philosophy, stack rationale |
| 2 | `architecture/01-bounded-contexts.md` | System decomposition, ownership, communication |
| 3 | `architecture/02-execution-model.md` | Run lifecycle, state machines, idempotency |
| 4 | `architecture/03-orchestration-model.md` | Scheduler, DAG execution, job queuing |
| 5 | `architecture/04-worker-model.md` | Worker pools, execution isolation, protocol |
| 6 | `architecture/05-event-model.md` | Event sourcing, outbox, audit trail |
| 7 | `architecture/06-reconciliation.md` | Drift detection, reconciliation loops |
| 8 | `architecture/07-security-model.md` | RBAC, secrets, policy enforcement |
| 9 | `architecture/08-scaling-failure.md` | Scaling strategy, failure handling |
| 10 | `adr/README.md` | All architectural decisions + rationale |

---

## AI Agent Implementation Roadmap

Phases are implemented in strict sequence. Each phase document is self-contained.

| Phase | File | Scope |
|-------|------|-------|
| 0 | `impl/phases/phase-0-foundation.md` | DB, core types, config, migrations, logging |
| 1 | `impl/phases/phase-1-stack-management.md` | Stack CRUD, VCS webhooks, variables |
| 2 | `impl/phases/phase-2-run-orchestration.md` | Run lifecycle, scheduler, job queue, state machine |
| 3 | `impl/phases/phase-3-worker-runtime.md` | Worker agent, Docker execution, heartbeat |
| 4 | `impl/phases/phase-4-policy-engine.md` | OPA integration, policy evaluation, enforcement |
| 5 | `impl/phases/phase-5-reconciliation.md` | Drift detection, reconciliation controller |
| 6 | `impl/phases/phase-6-event-sourcing.md` | Event store, outbox, NATS fan-out, audit log |

---

## Repository Entry Points

| Location | Purpose |
|----------|---------|
| `REPOSITORY.md` | Full repo structure + module ownership |
| `cmd/stratum-server/` | Control plane API server binary |
| `cmd/stratum-worker/` | Worker agent binary |
| `cmd/stratum-ctl/` | CLI tool |
| `internal/` | All bounded context implementations |
| `migrations/` | PostgreSQL migration files |
| `policies/` | Built-in OPA policy bundles |
| `deploy/` | Docker + Kubernetes deployment |

---

## Current Phase Status

> Update this table as phases complete.

| Phase | Status | Notes |
|-------|--------|-------|
| 0 — Foundation | `NOT STARTED` | |
| 1 — Stack Management | `NOT STARTED` | |
| 2 — Run Orchestration | `NOT STARTED` | |
| 3 — Worker Runtime | `NOT STARTED` | |
| 4 — Policy Engine | `NOT STARTED` | |
| 5 — Reconciliation | `NOT STARTED` | |
| 6 — Event Sourcing | `NOT STARTED` | |
