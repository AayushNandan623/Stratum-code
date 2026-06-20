# Stratum

**An infrastructure operating system. A cloud-native orchestration control plane.**

Stratum manages the complete lifecycle of infrastructure: planned changes, policy enforcement, drift detection, execution isolation, and continuous reconciliation — with full audit trail.

```
"What should my infrastructure look like, and how do I get there safely?"
```

Inspired by: Spacelift, Terraform Cloud, Argo Workflows, Kubernetes controller patterns, Atlantis, Temporal.

---

## Architecture at a Glance

```
┌───────────────────────────────────────────────────────────┐
│  Control Plane (stratum-server)                           │
│                                                           │
│  REST API + WebSocket  →  Domain Services                 │
│  Scheduler (DAG-aware) →  PostgreSQL Job Queue            │
│  Reconciler (drift)    →  Event Store (run events)        │
│  Policy Engine (OPA)   →  Secret Vault (AES-256)          │
│                                                           │
│  PostgreSQL  ──  NATS JetStream  ──  S3-compatible State  │
└───────────────────────────────────────────────────────────┘
                          │
           ┌──────────────┼──────────────┐
           ▼              ▼              ▼
    Hosted Workers   Private Workers  Private Workers
    (Docker)         (customer VPC)   (customer VPC)
```

**Language:** Go  |  **DB:** PostgreSQL  |  **Queue:** NATS JetStream  |  **IaC:** OpenTofu  |  **Policy:** OPA

---

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Stack** | A unit of infrastructure with a VCS source, variables, and desired state |
| **Run** | A single execution attempt: plan, apply, or drift-detect |
| **Worker** | Stateless executor that runs IaC tooling in an isolated Docker container |
| **Worker Pool** | A group of workers (hosted or customer-managed) assigned to stacks |
| **Drift** | Deviation between declared desired state and actual infrastructure state |
| **Reconciler** | Background controller that detects drift and triggers remediation |
| **Policy** | OPA rules evaluated before applies — can block, warn, or require approval |
| **Space** | Logical grouping of stacks for RBAC and organization |

---

## Quick Start (Local Development)

```bash
# Prerequisites: Go 1.22+, Docker
git clone https://github.com/yourorg/stratum
cd stratum

make dev-setup     # Start PostgreSQL + NATS, run migrations, seed dev data
make run-server    # Control plane on :8080
# In another terminal:
make run-worker    # Worker agent (connects to control plane)

# Create a stack and trigger a run
curl -X POST http://localhost:8080/api/v1/orgs/dev-org/stacks \
  -H "Authorization: Bearer dev-api-key" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-vpc", "vcs_repo": "github.com/yourorg/infra", "vcs_branch": "main"}'
```

---

## Documentation

| Audience | Start Here |
|----------|------------|
| **Human developer** (deep understanding) | [`docs/README.md`](docs/README.md) → Human Learning Roadmap |
| **AI coding agent** (implementation) | [`docs/impl/README.md`](docs/impl/README.md) |
| **Operations / deployment** | [`docs/ops/README.md`](docs/ops/README.md) |
| **Architecture decisions** | [`docs/adr/README.md`](docs/adr/README.md) |
| **Repository structure** | [`REPOSITORY.md`](REPOSITORY.md) |

---

## Development Status

| Phase | Description | Status |
|-------|-------------|--------|
| 0 | Foundation (DB, config, platform) | 🔲 Not started |
| 1 | Stack management, IAM, secrets | 🔲 Not started |
| 2 | Run orchestration, scheduler, DAG | 🔲 Not started |
| 3 | Worker runtime, Docker execution | 🔲 Not started |
| 4 | Policy engine (OPA) | 🔲 Not started |
| 5 | Reconciliation, drift detection | 🔲 Not started |
| 6 | NATS event bus, outbox, WebSocket scale | 🔲 Not started |

---

## Design Principles

- **Modular monolith first** — extract only when operationally justified
- **Desired-state primacy** — infrastructure state is continuously reconciled
- **Idempotency everywhere** — runs are retry-safe, events are deduplicated
- **Event sourcing for runs** — complete audit trail, replay, time-travel debugging
- **Policy at every boundary** — OPA evaluates before every apply
- **Execution isolation** — each run in a Docker container, no cross-run interference
- **Context-window-optimized codebase** — no god files, isolated domains, lean docs

---

## License

Apache 2.0
