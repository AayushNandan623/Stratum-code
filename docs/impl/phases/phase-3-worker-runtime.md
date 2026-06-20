# Phase 3: Worker Runtime

## Scope

**IN scope:**
- Worker registration, heartbeat, and deregistration API endpoints (`/api/v1/internal/workers/*`)
- Job claim long-poll endpoint (`GET /api/v1/internal/workers/{id}/jobs`)
- Run event ingestion from workers (`POST /api/v1/internal/runs/{id}/events`)
- Log chunk ingestion (`POST /api/v1/internal/runs/{id}/logs`)
- Source archive endpoint (`GET /api/v1/internal/runs/{id}/source-archive`)
- Secret one-time claim endpoint (`POST /api/v1/internal/runs/{id}/secrets/claim`)
- Worker pool management API (CRUD for pools, token generation)
- `stratum-worker` binary: full agent loop, Docker executor
- `internal/worker/` package: service, repository, executor interface, Docker implementation
- Hosted worker launcher: control plane spawns Docker containers for runs when pool type = HOSTED

**OUT of scope:**
- Policy evaluation (Phase 4 — worker calls policy gate stub that returns PASS)
- Kubernetes Job executor (post-Phase 3)
- NATS-based dispatch (Phase 6 — Phase 3 uses PostgreSQL long-poll)

---

## Prerequisites

Phase 2 complete and validated. Runs can reach QUEUED state. Run event API stubs exist.

---

## Files to Create / Modify

```
internal/worker/
  types.go              WorkerPool, Worker, Job domain types
  service.go            WorkerService interface + implementation
  repository.go         DB queries for workers and pools
  executor.go           Executor interface definition
  docker.go             DockerExecutor implementation

cmd/stratum-worker/
  main.go               Worker agent binary entry point
  agent/
    agent.go            Agent struct: registration, poll loop, execution
    protocol.go         HTTP client calls to control plane
    runner.go           Bridges Agent ↔ Executor

internal/api/handlers/
  workers.go            Worker pool management handlers
  internal_workers.go   Worker-facing internal API handlers
```

---

## Key Interfaces

```go
// internal/worker/executor.go

type Executor interface {
    Execute(ctx context.Context, task *ExecutionTask) (*ExecutionResult, error)
}

type ExecutionTask struct {
    RunID         uuid.UUID
    StackID       uuid.UUID
    OrgID         uuid.UUID
    RunType       RunType           // plan | apply | destroy | drift_detect
    WorkDir       string            // absolute path to extracted IaC source
    IACTool       string            // "opentofu"
    IACVersion    string            // "1.6.0"
    Env           []EnvVar          // ALL env vars including injected secrets
    StateBackend  StateBackendConfig // where to read/write tfstate
    LogCallback   func(line LogLine) // called for each log line as it arrives
    PlanFile      string            // path to plan.json (populated on apply from prior plan run)
}

type ExecutionResult struct {
    ExitCode    int
    PlanOutput  *PlanOutput  // populated on plan/drift_detect runs
    Error       string       // empty on success
}

type PlanOutput struct {
    Raw         []byte   // raw plan.json bytes
    HasChanges  bool
    Added       int
    Changed     int
    Removed     int
    Resources   []ResourceChange
}

type ResourceChange struct {
    Address string
    Actions []string  // ["create"] | ["update"] | ["delete"] | ["no-op"]
}

// internal/worker/service.go

type WorkerService interface {
    RegisterWorker(ctx context.Context, input RegisterWorkerInput) (*Worker, string, error) // returns worker, token
    Heartbeat(ctx context.Context, workerID uuid.UUID, status WorkerStatus) error
    Deregister(ctx context.Context, workerID uuid.UUID) error
    ClaimJob(ctx context.Context, workerID uuid.UUID, timeout time.Duration) (*Job, error)
    CompleteJob(ctx context.Context, jobID uuid.UUID, success bool) error

    CreatePool(ctx context.Context, input CreatePoolInput) (*WorkerPool, string, error) // returns pool, token
    GetPool(ctx context.Context, poolID uuid.UUID) (*WorkerPool, error)
    ListPools(ctx context.Context, orgID uuid.UUID) ([]*WorkerPool, error)
    DeletePool(ctx context.Context, poolID uuid.UUID) error
    RotatePoolToken(ctx context.Context, poolID uuid.UUID) (string, error)
}
```

---

## Worker Authentication Middleware

The internal API uses a different auth middleware than the user-facing API:

```go
// internal/api/middleware/worker_auth.go

func WorkerAuthMiddleware(workerSvc worker.WorkerService) Middleware {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // 1. Extract Bearer token
            token := extractBearerToken(r)
            if token == "" {
                writeError(w, 401, "missing worker token")
                return
            }
            // 2. Look up worker by HMAC of token
            wkr, err := workerSvc.GetByTokenHash(r.Context(), hashToken(token))
            if err != nil || wkr == nil {
                writeError(w, 401, "invalid worker token")
                return
            }
            // 3. Verify worker is registered (not deregistered/expired)
            if wkr.Status == StatusDeregistered {
                writeError(w, 401, "worker token revoked")
                return
            }
            // 4. Set worker identity on context
            ctx := context.WithValue(r.Context(), workerCtxKey, wkr)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

Token hashing: `HMAC-SHA256(token, STRATUM_WORKER_HMAC_SECRET)` — same HMAC secret as API keys. Tokens are never stored plaintext.

---

## Internal API Endpoints

All under `/api/v1/internal/` with worker auth middleware.

```
POST   /api/v1/internal/workers/register
  Body: { pool_id, hostname, version, capabilities[] }
  Auth: Pool token (Bearer wpt_xxx)
  Response: { worker_id, heartbeat_interval_s: 15 }

POST   /api/v1/internal/workers/{id}/heartbeat
  Body: { status: "IDLE"|"RUNNING", current_run_id: uuid|null }
  Response: { cancel_run_id: uuid|null }  ← control plane signals cancellation here

GET    /api/v1/internal/workers/{id}/jobs?timeout=30
  Long-poll: blocks up to 30s, returns immediately if job available
  Response: { job_id, run_id, run_type, stack_id, iac_tool, iac_version } | 204 No Content

DELETE /api/v1/internal/workers/{id}
  Deregisters worker gracefully

GET    /api/v1/internal/runs/{id}/source-archive
  Response: 302 redirect to signed S3 URL for git archive (tar.gz)
  OR: 200 with tar.gz body if no S3 (dev mode — control plane proxies from VCS)

POST   /api/v1/internal/runs/{id}/secrets/claim
  Body: { worker_id }
  Response: { secrets: [{name, value}] }  (one-time: 2nd call within 60s returns same values; after 60s: 404)

POST   /api/v1/internal/runs/{id}/events
  Body: { type, payload, occurred_at }
  Response: { seq }

POST   /api/v1/internal/runs/{id}/logs
  Body: { lines: [{line, source, occurred_at}] }
  Response: { accepted: N }
```

---

## Long-Poll Job Claim

The long-poll endpoint is the core of the pull-based dispatch model:

```go
// internal/api/handlers/internal_workers.go

func (h *InternalWorkerHandler) GetJob(w http.ResponseWriter, r *http.Request) {
    wkr := workerFromContext(r.Context())
    timeout := parseDuration(r.URL.Query().Get("timeout"), 30*time.Second)

    ctx, cancel := context.WithTimeout(r.Context(), timeout)
    defer cancel()

    for {
        job, err := h.workerSvc.ClaimJob(ctx, wkr.ID, 0) // non-blocking claim attempt
        if err != nil && !errors.Is(err, ErrNoJobAvailable) {
            writeError(w, 500, err.Error())
            return
        }
        if job != nil {
            writeJSON(w, 200, job.ToResponse())
            return
        }

        // No job available — wait and retry
        select {
        case <-ctx.Done():
            // Timeout expired — return empty 204
            w.WriteHeader(204)
            return
        case <-time.After(2 * time.Second):
            // Poll again
        }
    }
}
```

**Note:** In a high-scale environment, the 2-second poll loop creates DB pressure. Phase 6 replaces this with NATS push delivery. For Phase 3, the poll interval is acceptable.

---

## Docker Executor Implementation

```go
// internal/worker/docker.go — key logic (not full implementation)

type DockerExecutor struct {
    client    *docker.Client
    imageCache map[string]bool
    pullMu    sync.Mutex
}

func (e *DockerExecutor) Execute(ctx context.Context, task *ExecutionTask) (*ExecutionResult, error) {
    image := fmt.Sprintf("ghcr.io/opentofu/opentofu:%s", task.IACVersion)

    // 1. Ensure image is available
    if err := e.ensureImage(ctx, image); err != nil {
        return nil, fmt.Errorf("image pull failed: %w", err)
    }

    // 2. Build container config
    containerCfg := &container.Config{
        Image: image,
        Cmd:   e.buildCommand(task),
        Env:   e.buildEnv(task),
        User:  "10000:10000",
        WorkingDir: "/workspace",
    }
    hostCfg := &container.HostConfig{
        NetworkMode: container.NetworkMode("none"),
        Binds: []string{
            fmt.Sprintf("%s:/workspace:ro", task.WorkDir),
            fmt.Sprintf("%s:/state:rw", stateDir),
        },
        Resources: container.Resources{
            Memory:   512 * 1024 * 1024, // 512MB
            NanoCPUs: 1_000_000_000,     // 1 CPU
        },
    }

    // 3. Create and start container
    resp, err := e.client.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
    if err != nil { return nil, err }

    containerID := resp.ID
    defer e.client.ContainerRemove(context.Background(), containerID, container.RemoveOptions{Force: true})

    if err := e.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
        return nil, err
    }

    // 4. Stream logs
    logReader, _ := e.client.ContainerLogs(ctx, containerID, container.LogsOptions{
        ShowStdout: true, ShowStderr: true, Follow: true, Timestamps: false,
    })
    go e.streamLogs(logReader, task.LogCallback)

    // 5. Wait for completion
    statusCh, errCh := e.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
    select {
    case status := <-statusCh:
        result := &ExecutionResult{ExitCode: int(status.StatusCode)}
        if status.StatusCode == 0 && task.RunType == RunTypePlan {
            result.PlanOutput, _ = e.parsePlanOutput(stateDir)
        }
        return result, nil
    case err := <-errCh:
        return nil, err
    case <-ctx.Done():
        // Timeout or cancellation — send SIGTERM then SIGKILL
        e.client.ContainerStop(context.Background(), containerID, container.StopOptions{Timeout: ptr(30)})
        return &ExecutionResult{ExitCode: -1, Error: "execution cancelled"}, nil
    }
}

func (e *DockerExecutor) buildCommand(task *ExecutionTask) []string {
    switch task.RunType {
    case RunTypePlan:
        return []string{"plan", "-input=false", "-json", "-out=/state/plan.tfplan"}
    case RunTypeApply:
        return []string{"apply", "-input=false", "-json", "/state/plan.tfplan"}
    case RunTypeDestroy:
        return []string{"destroy", "-input=false", "-json", "-auto-approve"}
    case RunTypeDriftDetect:
        return []string{"plan", "-input=false", "-json", "-refresh-only"}
    }
    panic("unknown run type")
}
```

**Container images:** The `opentofu` image needs to be pre-built with the Stratum runner script that handles state backend configuration and credential setup. The `Dockerfile.worker-runner` is in `deploy/`. Workers pull this image, not the bare OpenTofu binary.

---

## Worker Agent Binary

```go
// cmd/stratum-worker/agent/agent.go

type Agent struct {
    config   Config
    workerID uuid.UUID
    client   *protocol.Client  // HTTP client to control plane
    executor worker.Executor
    stopCh   chan struct{}
}

func (a *Agent) Run(ctx context.Context) error {
    // Register
    workerID, err := a.client.Register(ctx, RegisterRequest{
        PoolID:       a.config.PoolID,
        Hostname:     hostname(),
        Version:      Version,
        Capabilities: []string{"opentofu"},
    })
    if err != nil { return fmt.Errorf("registration failed: %w", err) }
    a.workerID = workerID

    // Heartbeat goroutine
    go a.heartbeatLoop(ctx)

    // Graceful shutdown: complete current job
    defer a.deregister()

    // Job loop
    for {
        select {
        case <-ctx.Done():
            return nil
        default:
        }

        job, err := a.client.PollJob(ctx, a.workerID, 30*time.Second)
        if errors.Is(err, ErrNoJob) { continue }
        if err != nil {
            log.Error("job poll error", "err", err)
            time.Sleep(5 * time.Second)
            continue
        }

        if err := a.executeJob(ctx, job); err != nil {
            log.Error("job execution error", "run_id", job.RunID, "err", err)
        }
    }
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
    ticker := time.NewTicker(15 * time.Second)
    for {
        select {
        case <-ticker.C:
            resp, err := a.client.Heartbeat(ctx, a.workerID, a.currentStatus())
            if err != nil { log.Warn("heartbeat failed", "err", err) }
            // Check if control plane signalled cancellation
            if resp != nil && resp.CancelRunID != nil {
                a.cancelCurrentRun(*resp.CancelRunID)
            }
        case <-ctx.Done():
            return
        }
    }
}
```

---

## Source Code Checkout

Workers receive a signed URL to a Git archive. The control plane generates this:

```
Phase 3 implementation: control plane fetches archive from VCS provider on demand.
  1. GET /api/v1/internal/runs/{id}/source-archive
  2. Control plane uses VCS connection credentials to fetch archive from GitHub/GitLab
  3. Returns archive as streaming response (or redirect to temp S3 URL)

Phase 4+ optimization: archive is pre-fetched and cached in S3 on VCS push event.
```

For Phase 3, the control plane fetches on demand — simpler, slower, acceptable for low volume.

---

## State Backend Configuration

Workers need to know where to read/write Terraform state. The control plane injects this as environment variables:

```
TF_HTTP_ADDRESS=https://stratum.example.com/api/v1/stacks/{id}/state/tfstate
TF_HTTP_LOCK_ADDRESS=https://stratum.example.com/api/v1/stacks/{id}/state/lock
TF_HTTP_UNLOCK_ADDRESS=https://stratum.example.com/api/v1/stacks/{id}/state/lock
TF_HTTP_USERNAME=worker
TF_HTTP_PASSWORD={run-scoped-token}
```

Terraform's built-in `http` backend is used. Stratum's State API implements the Terraform HTTP backend protocol. This means no S3 bucket is required in Phase 3 — state is stored in PostgreSQL. S3 is added as an optional backend in Phase 4.

---

## Validation Criteria

After Phase 3:
1. `stratum-worker` starts, registers, shows as IDLE in `GET /api/v1/worker-pools/{id}/workers`
2. Create a run on a stack with a valid (but trivial) IaC config — run reaches APPLIED
3. Run logs visible in `GET /api/v1/runs/{id}/logs`
4. Run timeline shows full event sequence: `created → queued → assigned → planning_started → planned → applying_started → applied`
5. Kill worker process mid-plan → 60s later run re-queues → new worker picks it up
6. Kill worker process mid-apply → run moves to FAILED (not re-queued)
7. Cancel a running run via API → worker receives cancel signal via heartbeat response → Docker container stopped → run moves to CANCELLED
8. State file is stored: `GET /api/v1/stacks/{id}/state` returns metadata with version=1
