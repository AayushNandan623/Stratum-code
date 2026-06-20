# System Overview

## What Is Stratum?

Stratum is an **infrastructure operating system** — a control plane that treats infrastructure as a continuously managed system rather than a one-time deployment target.

It answers the question: *"What should my infrastructure look like, and how do I get it there safely, repeatably, and auditably?"*

Stratum coordinates:
- **Stacks** — units of infrastructure with declared desired state
- **Runs** — discrete executions that reconcile desired state with actual state
- **Workers** — isolated execution environments that perform the actual IaC operations
- **Policies** — guardrails that govern what can be applied, when, and by whom
- **Reconciliation** — continuous loops that detect drift and trigger corrective runs
- **Events** — a durable log of every action in the system

The mental model is: **Kubernetes for infrastructure workflows**. Like the K8s controller manager, Stratum continuously watches desired state, detects deviation, and dispatches reconciliation work. Unlike Kubernetes, Stratum is IaC-tool-agnostic and adds first-class concepts for approval workflows, cost governance, and audit compliance.

---

## Core Principles

**1. Desired-state primacy.**
Every stack has a declared desired state (Terraform config + variables). Every run is an attempt to reconcile actual state toward desired state. Drift is a first-class condition, not an exception.

**2. Execution isolation.**
Each run executes in an isolated environment (Docker container). No run can interfere with another. Workers are stateless.

**3. Event sourcing for runs.**
Every state transition in a run is an immutable event. The current state of a run is derived from its event history. This enables full audit trail, replay, and debugging.

**4. Idempotent operations.**
Runs are safe to retry. The system produces the same outcome if a run is re-executed with the same inputs. State locking prevents concurrent conflicting applies.

**5. Policy at every boundary.**
OPA policies evaluate before runs execute, before applies proceed, and at approval gates. Policy is data — stored, versioned, audited.

**6. Operational simplicity first.**
Start as a modular monolith. Introduce distributed components only when operationally justified.

---

## Technology Stack

### Why Go?
Control planes are Go's natural domain (Kubernetes, Temporal, NATS, Consul are all Go). Go provides: excellent concurrency primitives for orchestration loops, small single-binary deployments for workers, rich ecosystem for all required integrations, and strong typing for complex state machines.

### Why PostgreSQL as primary store?
PostgreSQL handles the event store (append-only events table), the job queue (SKIP LOCKED pattern), state locking (advisory locks), and all relational data. This is operationally one system to manage. No Redis, no Cassandra, no separate queue in Phase 0-2.

### Why NATS JetStream (Phase 2+)?
NATS JetStream provides persistent messaging with lower operational complexity than Kafka. It handles worker dispatch, domain event fan-out, and realtime WebSocket bridging. NATS is embedded-server-capable, enabling local dev with zero external dependencies.

### Why Temporal is deferred (Phase 3)?
Temporal is architecturally ideal for durable run workflows. However, it requires a Temporal server cluster, adding significant operational complexity early. Phase 0-2 implements run orchestration using PostgreSQL job queues. The `WorkflowEngine` interface is designed so Temporal can be swapped in at Phase 3 without changing run semantics.

### Why OPA embedded (not server)?
OPA has a Go SDK for in-process policy evaluation (no network hop, no external service dependency). Policies are loaded from the database. This is simpler to operate than a separate OPA server. OPA server can be introduced later for centralized policy management across multiple control plane instances.

### Why OpenTofu?
OpenTofu is the CNCF-hosted open-source fork of Terraform. It maintains full compatibility with existing Terraform configurations and has no BSL licensing restrictions. Workers execute `tofu plan` and `tofu apply` in isolated containers.

---

## High-Level Component Topology

```
┌──────────────────────────────────────────────────────────────┐
│                    Stratum Control Plane                      │
│                                                              │
│  ┌─────────────┐   ┌──────────────┐   ┌──────────────────┐  │
│  │  REST API   │   │  Scheduler   │   │  Reconciler      │  │
│  │  (HTTP/WS)  │   │  (job queue) │   │  (drift loops)   │  │
│  └──────┬──────┘   └──────┬───────┘   └──────┬───────────┘  │
│         │                 │                   │              │
│  ┌──────▼─────────────────▼───────────────────▼───────────┐  │
│  │                  Domain Services                        │  │
│  │  Stack | Run | Policy | IAM | State | Secret | VCS     │  │
│  └──────────────────────────┬────────────────────────────┘  │
│                             │                               │
│  ┌──────────────────────────▼────────────────────────────┐  │
│  │              PostgreSQL  +  S3-compatible state        │  │
│  └───────────────────────────────────────────────────────┘  │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐  │
│  │         NATS JetStream  (Phase 2+)                    │  │
│  └───────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
    ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
    │  Hosted      │  │  Private     │  │  Private     │
    │  Workers     │  │  Worker A    │  │  Worker B    │
    │  (Docker)    │  │  (customer)  │  │  (customer)  │
    └──────────────┘  └──────────────┘  └──────────────┘
```

---

## What Stratum Is NOT

- **Not a CI/CD system.** Stratum does not build code or run application tests. It manages infrastructure.
- **Not a Terraform wrapper.** The execution engine is pluggable. OpenTofu is the default, Pulumi and Ansible are planned.
- **Not a developer portal.** Stratum manages infrastructure lifecycle, not service catalogs or onboarding flows.
- **Not a monitoring system.** Stratum emits metrics and traces via OpenTelemetry. Grafana/Prometheus handle visualization.

---

## Differentiated Features (v1 Scope)

1. **Event-sourced run history** — Every run transition is a stored event. Full replay and time-travel debugging.
2. **DAG-based stack dependencies** — Stacks declare dependencies. The scheduler respects topological order.
3. **Embedded policy engine** — OPA evaluates policies inline with zero network latency.
4. **Private worker pools** — Customer infrastructure can run workers that connect to the control plane.
5. **Drift detection loop** — A background reconciler continuously compares desired vs actual state.
