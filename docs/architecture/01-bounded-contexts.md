# Bounded Contexts

Stratum is decomposed into 9 bounded contexts. Each context owns its domain model, its persistence, and its service logic. Communication between contexts is explicit and minimal.

---

## Context Map

```
┌────────────────────────────────────────────────────────────────────┐
│                          Core Platform                              │
│                                                                    │
│  ┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐  │
│  │   IAM    │────▶│  Stack   │────▶│   Run    │────▶│  Worker  │  │
│  │(identity)│     │(mgmt)    │     │(orchestr)│     │(runtime) │  │
│  └──────────┘     └──────────┘     └────┬─────┘     └──────────┘  │
│                                         │                          │
│  ┌──────────┐     ┌──────────┐          │          ┌──────────┐   │
│  │  Policy  │◀────│  State   │◀─────────┘          │  Secret  │   │
│  │ (engine) │     │ (remote) │                      │  (mgmt)  │   │
│  └──────────┘     └──────────┘                      └──────────┘   │
│                                                                    │
│  ┌──────────┐     ┌──────────┐     ┌──────────┐                   │
│  │  Events  │     │   VCS    │     │Reconcile │                   │
│  │  (bus)   │     │(integr.) │     │(drift)   │                   │
│  └──────────┘     └──────────┘     └──────────┘                   │
└────────────────────────────────────────────────────────────────────┘
```

---

## Context Definitions

### 1. IAM — Identity and Access Management

**Owns:** Organizations, Users, API keys, Roles, Role bindings, Sessions
**Responsibilities:** Authentication (JWT + API keys), RBAC enforcement, multi-tenancy boundaries
**Does NOT own:** Stack permissions (delegated to stack context via role binding references)
**Communicates via:** Direct service calls from API middleware; role bindings read by policy context

**Key invariants:**
- Every resource belongs to an organization
- Every API call carries a verified identity
- Role bindings are the source of truth for access decisions

---

### 2. Stack — Stack Management

**Owns:** Stacks, Stack configurations, Variable sets, VCS connections, Workspace mappings
**Responsibilities:** CRUD for stacks, variable management, VCS webhook dispatch, stack dependency graph
**Does NOT own:** Run execution (delegated to Run context), policy definitions (Policy context)
**Communicates via:** Publishes `stack.updated`, `stack.vcs_push` events; calls Run service to trigger runs

**Key invariants:**
- A stack has exactly one VCS repository + branch configuration
- Variable precedence: workspace override > variable set > stack default
- Dependency edges form a DAG (no cycles allowed — enforced on write)

---

### 3. Run — Run Orchestration

**Owns:** Runs, Run events (event store), Run logs, Approval requests, Job queue entries
**Responsibilities:** Run lifecycle state machine, scheduling, queuing, approval gates, log streaming
**Does NOT own:** Execution logic (Worker context), policy evaluation results (Policy context stores them as run events)
**Communicates via:** Publishes run events to Events context; calls Worker service for dispatch; calls Policy service pre-execution

**Key invariants:**
- A run's state is always derivable from its event log (event sourcing)
- Only one active run per stack at a time (enforced by state lock)
- Cancellation propagates to the worker immediately

**Run state machine:** → see `architecture/02-execution-model.md`

---

### 4. Worker — Worker Runtime

**Owns:** Worker registrations, Worker pool definitions, Worker heartbeats, Execution environments
**Responsibilities:** Worker lifecycle, pool management, run dispatch to workers, execution isolation (Docker)
**Does NOT own:** Run state (Run context owns it); workers report back via the Run context event API
**Communicates via:** Workers POST run events back to the Run context; Worker service publishes `worker.registered`, `worker.disconnected`

**Key invariants:**
- Workers are stateless — they receive a task, execute, and return results
- Worker tokens are short-lived and scoped to a single run
- All execution happens in a Docker container with no host network access by default

---

### 5. Policy — Policy Engine

**Owns:** Policy definitions, Policy sets, Policy evaluation results
**Responsibilities:** OPA bundle management, pre-run policy evaluation, guardrail enforcement
**Does NOT own:** Run outcomes — it produces pass/fail verdicts that Run context acts on
**Communicates via:** Called synchronously by Run context before execution; results stored as run events

**Key invariants:**
- Policy evaluation is synchronous and blocking (run cannot proceed without verdict)
- Policy bundles are versioned and attached to stacks
- Evaluation results are always stored, even on pass

---

### 6. State — Remote State Management

**Owns:** State files (metadata + S3 references), State versions, State locks
**Responsibilities:** Storing/retrieving Terraform state, state locking/unlocking, state version history
**Does NOT own:** Run execution — state is written by workers and read by reconciler
**Communicates via:** Workers write state directly via State API; Reconciler reads state for drift detection

**Key invariants:**
- State locks are mutually exclusive per stack (advisory lock in PostgreSQL)
- State files are never deleted, only versioned
- Lock acquisition must be atomic (no TOCTOU)

---

### 7. Reconcile — Drift Detection and Reconciliation

**Owns:** Reconciliation schedules, Drift records, Reconciliation runs (a special run type)
**Responsibilities:** Periodic desired-vs-actual comparison, drift record creation, triggering remediation runs
**Does NOT own:** State files (State context), Run execution (Run context)
**Communicates via:** Reads from State context; publishes `stack.drifted` events; calls Run context to create remediation runs

**Key invariants:**
- Reconciliation is read-only during drift detection — it never modifies infrastructure directly
- A stack's reconciliation interval is configurable per stack
- Reconciliation runs are labeled distinctly from user-triggered runs

---

### 8. Secret — Secret Management

**Owns:** Secret definitions (metadata), Encrypted secret values
**Responsibilities:** Secret CRUD, encryption at rest (AES-256), secret injection into worker environments
**Does NOT own:** Worker execution environments (Worker context injects secrets at execution time)
**Communicates via:** Worker context calls Secret service at run dispatch to retrieve decrypted values scoped to a run

**Key invariants:**
- Secret values are encrypted at rest with a key derived from a KMS-provided root key
- Secret values are never logged, never returned in API responses, never stored in plain text
- Secret injection happens at worker dispatch time, never at rest in the job queue

---

### 9. VCS — VCS Integration

**Owns:** VCS provider connections, Webhooks, PR checks
**Responsibilities:** Receiving Git push webhooks, creating PR status checks, parsing commit metadata
**Does NOT own:** Stack configuration (Stack context), Run creation (Run context)
**Communicates via:** On webhook receipt, calls Stack context to find affected stacks, then Run context to queue runs

**Key invariants:**
- Each VCS connection has a verified HMAC secret for webhook validation
- PR check statuses are updated as runs progress via run event subscriptions

---

## Context Communication Rules

**Rule 1: No direct database access across contexts.**
Each context reads only its own tables. Cross-context data is retrieved via service interfaces, never via JOIN queries.

**Rule 2: Events for async communication, direct calls for sync.**
When context A needs to react to context B's state change asynchronously, use events. When A needs B's data synchronously to proceed, use a service interface call.

**Rule 3: Shared IDs, not shared objects.**
Contexts share resource IDs (UUIDs) but never share domain objects. A Run knows its `stack_id` but calls the Stack service to get stack details — it does not import Stack domain types.

**Rule 4: Context boundary = package boundary.**
In the Go implementation, each context is `internal/<context>/`. Imports across context boundaries are only allowed through explicit service interfaces defined in each context's root package.
