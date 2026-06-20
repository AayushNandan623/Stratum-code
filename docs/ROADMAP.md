# Long-Term Roadmap and Extensibility

## Release Philosophy

Stratum is built in phases that each deliver working, usable functionality. No phase is a "refactor" or "cleanup" phase — every phase adds observable, testable value.

```
v0.1 — Phase 0+1: Stack management, auth, secrets, VCS webhooks
v0.2 — Phase 2:   Run orchestration, DAG scheduling, approval gates
v0.3 — Phase 3:   Worker runtime, Docker execution, end-to-end runs
v0.4 — Phase 4:   Policy engine, OPA integration, enforcement gates
v0.5 — Phase 5:   Reconciliation, drift detection, auto-remediation
v1.0 — Phase 6:   NATS event bus, multi-instance WebSocket, audit log
```

Post-v1 development follows the RFC process.

---

## Post-v1 Feature Roadmap

### Tier 1: High Value, Moderate Complexity

**T1.1 — Run History Replay API**
Expose a `POST /api/v1/runs/{id}/replay` endpoint that creates a new run seeded from a historical run's exact configuration snapshot (commit SHA, variable values at the time, IaC version). This completes the event sourcing value: not just audit, but reproducibility.

*Required changes:* Store configuration snapshot at run creation time (commit SHA already stored as `config_version`). Add variable set snapshot to run record. New run type: `REPLAY`.

**T1.2 — Stack Configuration Versioning**
Every change to a stack's configuration (variables, VCS settings, worker pool) is version-controlled. Operators can view configuration history and diff between versions. Runs reference the configuration version at trigger time.

*Required changes:* `stack_config_versions` table (append-only). Modify stack update to create a new version record. Run creation stores `config_version_id`.

**T1.3 — Pull Request Plan Posting**
When a VCS push event is from a pull request, post the plan output as a PR comment (GitHub/GitLab). Include resource change summary, policy verdict, and cost estimate (if enabled). Add a "Apply" button that triggers an apply run when the PR is merged.

*Required changes:* Extend VCS context with GitHub API client for PR comments. Add PR metadata to push events. New run trigger type: `pr_merged`.

**T1.4 — Worker Pool Auto-Scaling (Hosted)**
Hosted worker pools auto-scale based on job queue depth. Add a `scaling_config` to worker pools: min workers, max workers, scale-up threshold (queue depth), scale-down cooldown.

*Required changes:* Hosted worker launcher goroutine with scaling logic. Worker health tracking for scale-down decisions. New metrics: `stratum_pool_scale_events_total`.

---

### Tier 2: High Value, High Complexity

**T2.1 — Progressive Infrastructure Rollouts (RFC-001)**
Apply infrastructure changes in staged rollouts across regions/environments with automated health gates. See `docs/adr/rfcs.md#rfc-001` for full design.

*Required changes:* New `rollout_plans` and `rollout_stages` tables. New `RolloutRun` type. New state machine for rollout orchestration. HealthGate interface (webhook, CloudWatch, custom command).

**T2.2 — Temporal Workflow Engine (RFC-002)**
Replace PostgreSQL scheduler with Temporal for durable workflow execution. See `docs/adr/rfcs.md#rfc-002`.

*Required changes:* Temporal client setup. Workflow definitions for plan/apply/rollout. Migration of in-flight runs. `WorkflowEngine` interface implementation swap.

**T2.3 — Multi-IaC Tool Support (RFC-003)**
Pulumi and Ansible executor support. See `docs/adr/rfcs.md#rfc-003`.

*Required changes:* `ToolRuntime` interface. `PulumiRuntime`, `AnsibleRuntime` implementations. Plan output parsing per tool. State backend abstraction for Pulumi.

**T2.4 — Ephemeral Stack Environments (RFC-004)**
On each pull request, automatically create a complete copy of a stack's infrastructure in an isolated namespace/account, run the plan against it, and tear it down on PR close. Full infrastructure preview environments.

*Required changes:* Ephemeral stack concept (stack with `lifecycle=ephemeral`, `parent_stack_id`, `ttl`). TTL-based cleanup controller. Ephemeral stack namespace isolation per IaC tool. PR close webhook → destroy run trigger.

---

### Tier 3: Moderate Value, Low Complexity (Quick Wins)

**T3.1 — Slack and PagerDuty Notifications**
Extend notification router with real integrations: Slack (already stubbed), PagerDuty, email via SMTP, webhook. Configurable per org, per space, or per stack with event filters (notify on failed runs only, drift only, etc.).

**T3.2 — Cost Estimation (Infracost Integration)**
Before apply, run `infracost diff` against the plan output. Store cost estimate as a run event. Display cost delta in UI. Policy rules can reference cost delta (e.g., "block if monthly cost increase > $1000").

*Required changes:* New worker phase between `PLANNED` and policy evaluation: cost estimation. Infracost binary in worker runner image. `cost.estimated` run event type. Policy input document extended with `cost` object.

**T3.3 — Stack Templates**
Pre-built stack templates that users can instantiate. Templates define: IaC source (built-in or VCS), required variables (with types and validation), recommended worker pool, suggested policies. Reduces time-to-first-run for common patterns (EKS cluster, RDS instance, etc.).

**T3.4 — CLI Tool (`stratum-ctl`)**
A full-featured CLI for operators and power users. Commands: `stack create`, `run trigger`, `run logs --follow`, `drift list`, `policy upload`, `worker-pool create`, `state pull`. Authentication via API key stored in `~/.config/stratum/credentials`.

**T3.5 — Audit Log Export**
API endpoint to export audit log entries for a date range and org to CSV or NDJSON. Required for compliance reporting (SOC2, ISO27001 evidence collection). Paginated, filterable by actor, resource type, action.

---

## Extensibility Design Points

These are the extension points deliberately designed into the system for future flexibility.

### Executor Interface
`internal/worker/executor.go` — adding a new IaC tool or execution environment (Kubernetes Jobs, Firecracker VMs) requires only a new `Executor` implementation. The worker agent and control plane are unchanged.

### EventBus Interface
`internal/events/bus.go` — the event bus implementation can be swapped without changing any domain code. Currently: in-memory (test) or NATS. Could be replaced with Kafka, Pulsar, or Google Pub/Sub by implementing `EventBus`.

### StorageBackend Interface
`internal/state/storage.go` — state file storage is abstracted. Currently: PostgreSQL (Phase 3) or S3-compatible (Phase 4+). Could be extended to GCS, Azure Blob, or any object store.

### WorkflowEngine Interface
`internal/run/scheduler.go` — the scheduler implements a `WorkflowEngine` interface. Temporal replaces the PostgreSQL-based implementation without changing run domain logic.

### PolicyEvaluator Interface
`internal/policy/evaluator.go` — OPA is the default. Checkov, Conftest, or a custom evaluator can be plugged in. Multiple evaluators can run in parallel (fan-out, collect all violations).

### VCS Provider Interface
`internal/vcs/service.go` — each VCS provider (GitHub, GitLab, Bitbucket, Azure DevOps) is a separate implementation of the provider interface. Adding a new VCS requires implementing: webhook validation, push event parsing, PR status posting, and archive download.

### ToolRuntime Interface (RFC-003)
`internal/worker/executor.go` (future) — each IaC tool (OpenTofu, Pulumi, Ansible, CDK) is a `ToolRuntime`. The Docker executor is tool-agnostic; it delegates command construction and output parsing to the runtime.

---

## Kubernetes-Native Path

Post-v1, Stratum can become Kubernetes-native by:

1. **Control plane deployment on K8s** — already containerized, just add Helm chart
2. **Worker pool as K8s Jobs** — new `KubernetesJobExecutor` behind the `Executor` interface
3. **State storage on PVC** — already abstracted behind `StorageBackend`
4. **Leader election via Kubernetes leases** — replace PostgreSQL advisory locks

This is a **deployment** change, not an architecture change. The bounded context structure, interfaces, and event model remain identical.

---

## Observability Maturity Path

```
Phase 0-3:  Structured logs (slog) + basic Prometheus counters
Phase 4-5:  Full OpenTelemetry trace propagation, Grafana dashboards
Phase 6:    NATS consumer lag metrics, outbox backlog alerting
Post-v1:    Distributed tracing across worker + control plane
            Custom Grafana dashboards per org (multi-tenant observability)
            SLO burn-rate alerts
```

---

## Security Maturity Path

```
Phase 0-3:  API key + JWT auth, RBAC, AES-256 secrets at rest
Phase 4:    OPA policy enforcement at apply gate
Phase 5-6:  Full audit log via NATS archiver
Post-v1:    OIDC/SAML SSO (Google Workspace, Okta, Azure AD)
            FIDO2/WebAuthn for UI login
            HSM-backed key management (AWS KMS, GCP KMS)
            mTLS between control plane and private workers
            Network policy enforcement per worker pool
            Supply chain security: worker image signing (Sigstore)
```

---

## Scale Checkpoints

Design decisions to revisit at specific scale thresholds:

| Threshold | Decision Point |
|-----------|---------------|
| 100 concurrent runs | Evaluate NATS dispatch vs PostgreSQL long-poll |
| 500 stacks per org | Evaluate reconcile schedule partitioning |
| 10,000 stacks total | Evaluate PostgreSQL connection pooling, read replicas |
| 1M run events/day | Evaluate run_events partitioning by time range |
| 100 API instances | Evaluate scheduler extraction to standalone process |
| 10 regions | Evaluate regional worker pools with control plane federation |
