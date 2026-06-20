# Worker Model

## What Is a Worker?

A Worker is a stateless process that claims a run job, executes the IaC tooling in an isolated Docker container, streams logs back to the control plane, and reports the outcome via the Run event API.

Workers never hold persistent state. They are intentionally dumb executors. All orchestration intelligence lives in the control plane.

---

## Worker Types

### Hosted Workers
Managed by Stratum. Run as Docker containers on the control plane host (Phase 0-3) or as Kubernetes Jobs (Phase 4+). The control plane launches hosted workers on-demand when a run is queued and terminates them when the run completes.

### Private Workers
Operated by the user in their own infrastructure. A private worker is a long-running process that connects outbound to the control plane, authenticates with a pool token, and polls for work. The control plane never initiates inbound connections to private workers — this is critical for customers with private VPC infrastructure.

Private workers enable:
- Execution within customer VPCs (where Terraform needs private network access to infrastructure)
- Custom tooling versions (specific Terraform/OpenTofu versions)
- Custom credential environments (AWS IAM roles, Vault agent, etc.)

---

## Worker Pool

A Worker Pool is a logical grouping of workers with a shared configuration. Stacks are assigned to a pool. All runs for a stack execute on workers from its assigned pool.

```
Table: worker_pools
──────────────────────────────────────────
id              UUID
org_id          UUID
name            STRING
type            ENUM    HOSTED | PRIVATE
token_hash      STRING  HMAC-SHA256 of registration token
max_concurrency INT     max parallel runs
labels          JSONB   arbitrary key-value metadata
created_at      TIMESTAMPTZ
```

---

## Worker Registration Protocol

```
1. Operator generates pool token via API
   POST /api/v1/worker-pools/{pool_id}/tokens
   → returns: { token: "wpt_xxx...xxx" }  (shown once, never stored plaintext)

2. Worker process starts with token in environment
   STRATUM_WORKER_TOKEN=wpt_xxx...xxx
   STRATUM_CONTROL_PLANE_URL=https://stratum.example.com

3. Worker POSTs registration
   POST /api/v1/workers/register
   { pool_id, version, capabilities: ["opentofu", "pulumi"], hostname }
   Authorization: Bearer wpt_xxx...xxx
   → returns: { worker_id, heartbeat_interval_s: 15 }

4. Worker enters polling loop:
   GET /api/v1/workers/{worker_id}/jobs?timeout=30
   (long-poll, returns immediately if job available)

5. On job receipt, worker starts execution (see below)

6. Worker sends heartbeat every 15s:
   POST /api/v1/workers/{worker_id}/heartbeat
   { current_job_id: "r-xxx" | null, status: "IDLE" | "RUNNING" }
```

---

## Execution Isolation

Each run executes in a Docker container created by the worker. The worker process itself is lightweight — it manages the container lifecycle.

**Container specification per run:**
```
Image:          stratum/runner-opentofu:1.6.0 (or customer-specified)
Network:        none (isolated) unless stack declares network_mode
Mounts:         /workspace (source code, read-only bind mount from checkout)
                /state     (working directory for plan/state files)
Env:            Injected secrets (see Secret model)
                TF_VAR_* variables from stack variable set
                STRATUM_RUN_ID, STRATUM_STACK_ID (metadata)
CPU/Memory:     Configurable per pool (default: 1 CPU, 512MB)
User:           Non-root (UID 10000)
Read-only root: Yes (except /workspace and /state)
```

**Why Docker-in-worker (not Kubernetes Jobs)?**
Docker is available everywhere — VMs, laptop, CI runners. Kubernetes Jobs require a cluster. In Phase 0-3, Docker is the execution primitive. Phase 4 adds a `KubernetesJobExecutor` behind the same `Executor` interface.

---

## Worker-to-Control-Plane Communication

Workers communicate exclusively via the Stratum HTTP API. They never write directly to the database.

**Run execution protocol:**

```
Phase 1: Source checkout
  Worker calls: GET /api/v1/runs/{run_id}/source-archive
  → returns: signed URL to Git archive (tarball)
  Worker downloads and extracts to /workspace

Phase 2: Secrets injection
  Worker calls: POST /api/v1/runs/{run_id}/secrets/claim
  → returns: { secrets: [{ name, value }] }  (one-time claim, expires in 60s)
  Worker writes secrets to container env — never to disk

Phase 3: Execution
  Worker starts Docker container
  Container streams stdout/stderr line-by-line:
    POST /api/v1/runs/{run_id}/logs  { lines: [...] }
  Worker sends progress events:
    POST /api/v1/runs/{run_id}/events  { type: "run.planning_started" }

Phase 4: Plan output
  Container writes plan.json to /state
  Worker uploads: PUT /api/v1/runs/{run_id}/plan-output  (multipart)

Phase 5: Completion
  Worker sends terminal event:
    POST /api/v1/runs/{run_id}/events  { type: "run.applied" | "run.failed", payload: {...} }
  Worker destroys Docker container
  Worker returns to polling loop
```

---

## Cancellation Propagation

When a user cancels a run:
1. Control plane marks run `CANCELLED` (writes cancel event to event store)
2. Worker's next heartbeat response includes `{ cancel: true, run_id: "..." }`
3. Worker sends `SIGTERM` to the Docker container
4. Container has 30s to clean up (Terraform performs state cleanup on SIGTERM)
5. After 30s, worker sends `SIGKILL`
6. Worker reports `run.cancelled` event

For runs in `QUEUED` state (not yet claimed), cancellation simply removes the job from the queue — no worker action needed.

---

## Worker Health and Fault Detection

```
Heartbeat interval:    15s  (worker → control plane)
Claim expiry:          60s  (if no heartbeat, claim is released)
Dead worker detection: Scheduler marks worker DISCONNECTED after 3 missed heartbeats (45s)
Recovery:              Run returns to QUEUED, attempt counter incremented
Max attempts:          3 (configurable per pool)
```

**Mid-apply crash handling:**
If a worker dies during APPLYING state, the run moves to `FAILED` rather than being re-queued. This is because partial Terraform applies may have occurred. The control plane creates a drift record for manual review. An operator can trigger a new run after reviewing the situation.
