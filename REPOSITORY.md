# Repository Structure

## Overview

```
stratum/
├── cmd/                          # Binary entry points
│   ├── stratum-server/           # Control plane API + scheduler + reconciler
│   ├── stratum-worker/           # Worker agent binary
│   └── stratum-ctl/              # Operator CLI
│
├── internal/                     # Private implementation (no external import)
│   ├── api/                      # HTTP API layer (routing, middleware, handlers)
│   │   ├── middleware/           # Auth, RBAC, logging, rate-limit middleware
│   │   ├── handlers/             # One file per resource (stacks, runs, workers, ...)
│   │   └── ws/                   # WebSocket upgrade and subscription handling
│   │
│   ├── iam/                      # Bounded context: Identity and Access Management
│   │   ├── service.go            # IAM service interface + implementation
│   │   ├── repository.go         # DB queries
│   │   └── types.go              # Domain types (User, APIKey, RoleBinding, ...)
│   │
│   ├── stack/                    # Bounded context: Stack Management
│   │   ├── service.go
│   │   ├── repository.go
│   │   ├── graph.go              # DAG cycle detection, topological sort
│   │   └── types.go
│   │
│   ├── run/                      # Bounded context: Run Orchestration
│   │   ├── service.go
│   │   ├── repository.go
│   │   ├── statemachine.go       # State transition validation and event emission
│   │   ├── scheduler.go          # Scheduler goroutine
│   │   ├── eventstore.go         # Run event append and query
│   │   └── types.go
│   │
│   ├── worker/                   # Bounded context: Worker Runtime
│   │   ├── service.go
│   │   ├── repository.go
│   │   ├── executor.go           # Executor interface (Docker, K8s, ...)
│   │   ├── docker.go             # Docker executor implementation
│   │   └── types.go
│   │
│   ├── policy/                   # Bounded context: Policy Engine
│   │   ├── service.go
│   │   ├── repository.go
│   │   ├── evaluator.go          # OPA evaluation logic
│   │   ├── loader.go             # Bundle loading and hot-reload
│   │   └── types.go
│   │
│   ├── state/                    # Bounded context: Remote State
│   │   ├── service.go
│   │   ├── repository.go
│   │   ├── storage.go            # Storage backend interface
│   │   ├── s3.go                 # S3-compatible storage implementation
│   │   └── types.go
│   │
│   ├── secret/                   # Bounded context: Secret Management
│   │   ├── service.go
│   │   ├── repository.go
│   │   ├── crypto.go             # AES-256-GCM encrypt/decrypt
│   │   └── types.go
│   │
│   ├── reconcile/                # Bounded context: Drift Detection
│   │   ├── service.go
│   │   ├── repository.go
│   │   ├── controller.go         # Reconciler loop goroutine
│   │   └── types.go
│   │
│   ├── vcs/                      # Bounded context: VCS Integration
│   │   ├── service.go
│   │   ├── webhook.go            # Webhook receiver and validation
│   │   ├── github.go             # GitHub provider
│   │   ├── gitlab.go             # GitLab provider
│   │   └── types.go
│   │
│   ├── events/                   # Event bus abstraction
│   │   ├── bus.go                # EventBus interface
│   │   ├── nats.go               # NATS JetStream implementation
│   │   ├── inmemory.go           # In-memory implementation (testing/Phase 0)
│   │   └── outbox.go             # Transactional outbox relay
│   │
│   ├── platform/                 # Shared infrastructure
│   │   ├── db/                   # DB connection, transactions, helpers
│   │   ├── config/               # Configuration loading (env vars + file)
│   │   ├── logger/               # Structured logger setup
│   │   ├── telemetry/            # OpenTelemetry setup (metrics, traces)
│   │   └── clock/                # Clock abstraction (for testability)
│   │
│   └── testhelpers/              # Test utilities (NOT test files)
│       ├── fixtures.go           # Test data builders
│       └── dbtest.go             # In-transaction test DB setup
│
├── migrations/                   # PostgreSQL migration files
│   ├── 001_init_iam.sql
│   ├── 002_init_stacks.sql
│   ├── 003_init_runs.sql
│   ├── 004_init_workers.sql
│   ├── 005_init_policy.sql
│   ├── 006_init_state.sql
│   ├── 007_init_secrets.sql
│   ├── 008_init_reconcile.sql
│   ├── 009_init_events_outbox.sql
│   └── 010_init_vcs.sql
│
├── policies/                     # Built-in OPA policy bundles
│   ├── built-in/
│   │   ├── no-public-storage.rego
│   │   ├── require-tags.rego
│   │   └── resource-limits.rego
│   └── examples/
│       └── cost-guardrail.rego
│
├── deploy/                       # Deployment configurations
│   ├── docker-compose.yml        # Local development stack
│   ├── docker-compose.prod.yml   # Production-ready compose (with replicas)
│   ├── Dockerfile.server         # Control plane image
│   ├── Dockerfile.worker         # Worker agent image
│   └── k8s/                      # Kubernetes manifests (Phase 4+)
│       ├── control-plane/
│       └── worker-pool/
│
├── docs/                         # All documentation (see docs/README.md)
│
├── scripts/                      # Developer convenience scripts
│   ├── dev-setup.sh              # One-command local environment setup
│   ├── migrate.sh                # Run migrations
│   └── seed.sh                   # Seed development data
│
├── go.mod
├── go.sum
├── Makefile                      # Build, test, lint, migration targets
└── README.md                     # Project overview and quick start
```

---

## Module Ownership Map

| Module | Owner Context | Phase Introduced | Key Interfaces |
|--------|--------------|-----------------|----------------|
| `internal/iam` | IAM | Phase 0 | `IAMService`, `Authenticator` |
| `internal/stack` | Stack | Phase 1 | `StackService`, `DependencyGraph` |
| `internal/run` | Run | Phase 2 | `RunService`, `Scheduler`, `EventStore` |
| `internal/worker` | Worker | Phase 3 | `WorkerService`, `Executor` |
| `internal/policy` | Policy | Phase 4 | `PolicyService`, `Evaluator` |
| `internal/state` | State | Phase 1 | `StateService`, `StorageBackend` |
| `internal/secret` | Secret | Phase 1 | `SecretService`, `Crypter` |
| `internal/reconcile` | Reconcile | Phase 5 | `ReconcileService`, `DriftController` |
| `internal/vcs` | VCS | Phase 1 | `VCSService`, `WebhookHandler` |
| `internal/events` | Platform | Phase 2 | `EventBus`, `OutboxRelay` |
| `internal/platform` | Platform | Phase 0 | `DB`, `Config`, `Logger`, `Telemetry` |
| `internal/api` | Platform | Phase 0 | `Router`, `Middleware` |

---

## Import Rules

These are enforced via `go vet` or a custom linter rule:

```
ALLOWED cross-context imports:
  any context → internal/platform/*
  api/handlers → any context service interface
  run → stack (via StackService interface only)
  reconcile → run (via RunService interface only)
  reconcile → state (via StateService interface only)
  worker → secret (via SecretService interface only, at dispatch time)

FORBIDDEN:
  context A → context B's repository directly
  context A → context B's types.go (use shared interfaces or DTOs)
  any context → api/* (API layer depends on contexts, not vice versa)
```

---

## Key File Naming Conventions

| Pattern | Purpose |
|---------|---------|
| `types.go` | Domain types for the context (structs, enums, errors) |
| `service.go` | Service interface definition + implementation struct |
| `repository.go` | Database queries (SQL, no business logic) |
| `*_test.go` | Tests adjacent to the file they test |
| `internal/platform/db/` | DB helpers; context repos import this, never `database/sql` directly |

---

## Binary Responsibilities

### `stratum-server`
Starts: HTTP API, Scheduler goroutine, Reconciler goroutine, Outbox relay goroutine, WebSocket hub.
Connects to: PostgreSQL, NATS (Phase 2+), S3 (for state storage), Docker (for hosted workers, Phase 3+).

### `stratum-worker`
Starts: Worker registration, long-poll job loop, Docker executor.
Connects to: Stratum control plane API only (outbound HTTP).
Does NOT connect to: PostgreSQL, NATS, S3 directly.

### `stratum-ctl`
CLI tool for operators. Operations: org management, worker pool management, manual run triggers, policy upload, state inspection.
Connects to: Stratum API (authenticated via API key or user session).
