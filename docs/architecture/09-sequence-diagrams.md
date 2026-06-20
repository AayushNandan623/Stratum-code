# Key Flow Sequence Diagrams

Reference diagrams for the three most important flows in Stratum.
Use these when debugging, designing integrations, or onboarding.

---

## Flow 1: VCS Push → Run Applied

```
Developer          GitHub            VCS Context          Stack Context
    │                 │                    │                    │
    │── git push ────▶│                    │                    │
    │                 │── POST /webhooks/github ──────────────▶│
    │                 │                    │                    │
    │                 │          ValidateHMAC(signature)        │
    │                 │          ParsePushEvent()               │
    │                 │          return PushEvent{repo,branch,sha}
    │                 │                    │                    │
    │                 │         FindStacksByVCS(repo, branch)──▶│
    │                 │                    │◀── [stack-id-1]────│
    │                 │                    │                    │
    │                 │         emit: stack.vcs_push{stackID,sha}
    │                 │                    │                    │
    │◀── 200 OK ──────│                    │                    │


Run Context          Scheduler           Worker Pool           Worker
    │                    │                    │                    │
    │◀── CreateRun(stackID, trigger=vcs_push)
    │    RunType=plan (or apply if auto_apply=true)
    │
    │── INSERT runs(PENDING) ──────────────────────────────────▶DB
    │── INSERT run_events(run.created, seq=1)
    │── INSERT outbox_messages(stratum.runs.events.{id})
    │
    │                    │ (scheduler tick, 5s)
    │                    │── ListPending()
    │                    │◀── [run-id-1]
    │                    │── isBlocked? (DAG check) → no
    │                    │── hasActiveRun(stackID)? → no
    │                    │── INSERT run_jobs(AVAILABLE)
    │── Transition(QUEUED) ← scheduler
    │── INSERT run_events(run.queued, seq=2)
    │
    │                              │ (worker long-poll returns)
    │                              │◀── job claimed (SKIP LOCKED)
    │── Transition(ASSIGNED) ←────│
    │── INSERT run_events(run.assigned, seq=3)
    │
    │                                                   │
    │── GET /internal/runs/{id}/source-archive ────────▶│
    │◀── tar.gz archive (git checkout)
    │── POST /internal/runs/{id}/secrets/claim ─────────│
    │◀── [{name, value}]
    │
    │── Transition(PLANNING) ←────────────────────────▶│
    │── INSERT run_events(run.planning_started, seq=4)
    │
    │                                         Docker container starts
    │                                         `tofu plan -json -out plan.tfplan`
    │── POST /internal/runs/{id}/logs ─────────│ (streaming, batched)
    │                                         plan finishes
    │── PUT /internal/runs/{id}/plan-output ──│
    │── POST /internal/runs/{id}/events {run.planned}
    │── Transition(PLANNED)
    │── INSERT run_events(run.planned, seq=5)
    │
    │ (scheduler evaluates policy gate)
    │── PolicyService.Evaluate(run, planOutput)
    │◀── {allow: true, violations: []}
    │── INSERT run_events(policy.evaluated, seq=6)
    │
    │ (auto_apply=true OR approval received)
    │── Transition(APPLYING)
    │── INSERT run_events(run.applying_started, seq=7)
    │
    │                                         `tofu apply plan.tfplan -json`
    │── POST /internal/runs/{id}/logs ─────────│ (streaming)
    │                                         apply finishes
    │── POST /internal/runs/{id}/events {run.applied}
    │── Transition(APPLIED)
    │── INSERT run_events(run.applied, seq=8)
    │── UPDATE stacks SET status=ACTIVE
    │
    │ (reconcile context callback)
    │── ReconcileService.ResolveDrift(stackID)
    │
    │ outbox relay picks up events
    │── NATS publish: stratum.runs.events.{run-id} (each event)
    │
    │ WebSocket clients connected to this run receive all events in order
```

---

## Flow 2: Drift Detection and Auto-Remediation

```
Reconciler           ReconcileService     RunService           Worker
Controller
    │                    │                    │                    │
    │ (every 10s tick)
    │── ClaimNextDue(SKIP LOCKED) ───────────────────────────▶DB
    │◀── schedule{stackID, interval=1h}
    │
    │── processStack(stackID)
    │── stackSvc.Get(stackID)
    │── runSvc.HasActiveRun(stackID)? → false
    │
    │── runSvc.Create({RunTypeDriftDetect, TriggerTypeSchedule})
    │                    │── INSERT runs(PENDING)
    │                    │── INSERT run_events(run.created)
    │
    │ (scheduler tick)
    │── run → QUEUED → ASSIGNED → PLANNING
    │
    │                                                   │
    │                                         `tofu plan -refresh-only -json`
    │                                         Output:
    │                                         resource_changes: [
    │                                           {address: "aws_sg.main", actions: ["update"]}
    │                                         ]
    │                                                   │
    │── POST /internal/runs/{id}/events {run.planned, planOutput}
    │── Transition(PLANNED) [drift_detect runs skip policy/approval]
    │── Transition(APPLIED) [drift_detect runs have no apply phase]
    │
    │ run service callback:
    │── ReconcileService.ProcessDriftResult(runID, planOutput)
    │                    │
    │                    │── planOutput.HasChanges = true → drift!
    │                    │── INSERT drift_records(DETECTED)
    │                    │── stackSvc.SetStatus(DRIFTED)
    │                    │── emit: stack.drifted{stackID, driftRecordID}
    │                    │
    │                    │── schedule.DriftMode = AUTO_APPLY?
    │                    │── runSvc.Create({RunTypeApply, TriggerTypeDrift})
    │                    │── UPDATE drift_records SET remediation_run_id = run.ID
    │
    │ (remediation apply run executes normally)
    │ (on APPLIED, ReconcileService.ResolveDrift called)
    │                    │── UPDATE drift_records SET status=RESOLVED
    │                    │── stackSvc.SetStatus(ACTIVE)
    │                    │── emit: drift.resolved{stackID}
    │
    │ NATS notification router consumer receives stack.drifted:
    │── POST Slack webhook: "Drift detected on stack prod-vpc: 1 resource changed"
```

---

## Flow 3: Worker Registration and Job Claim Protocol

```
stratum-worker        Control Plane API    WorkerService         JobQueue(DB)
    │                        │                    │                    │
    │ startup
    │── POST /internal/workers/register ─────────▶│
    │   {pool_id, hostname, version, capabilities}│
    │   Authorization: Bearer wpt_xxx             │
    │                        │── ValidatePoolToken │
    │                        │── INSERT workers(IDLE)
    │                        │── return worker_id  │
    │◀── {worker_id, heartbeat_interval_s: 15}    │
    │
    │ start heartbeat goroutine (15s ticker)
    │ start job poll loop
    │
    │── GET /internal/workers/{id}/jobs?timeout=30
    │                        │── attempt ClaimJob (SKIP LOCKED)
    │                        │── no job available → hold connection
    │                        │   (long-poll: check every 2s for 30s)
    │
    │                        │ (scheduler enqueues a job)
    │                        │── SKIP LOCKED claim succeeds
    │                        │── UPDATE run_jobs SET status=CLAIMED, claimed_by=worker_id
    │                        │── Transition run: QUEUED → ASSIGNED
    │◀── {job_id, run_id, run_type, stack_id, iac_tool, iac_version}
    │
    │── GET /internal/runs/{id}/source-archive
    │◀── tar.gz (proxied from VCS)
    │── extract to /tmp/stratum-runs/{run_id}/
    │
    │── POST /internal/runs/{id}/secrets/claim
    │◀── [{name: "AWS_ACCESS_KEY_ID", value: "AKIA..."}, ...]
    │── build Docker env vars from secrets
    │
    │── POST /internal/runs/{id}/events {type: run.planning_started}
    │
    │── DockerExecutor.Execute(task)
    │   ├── ContainerCreate (image, env, mounts)
    │   ├── ContainerStart
    │   ├── ContainerLogs(Follow=true) → goroutine
    │   │   └── POST /internal/runs/{id}/logs [{line, source}] (batched)
    │   └── ContainerWait(NotRunning)
    │
    │── parse plan.json → PlanOutput
    │── PUT /internal/runs/{id}/plan-output (multipart)
    │── POST /internal/runs/{id}/events {type: run.planned, payload: {changes: 3}}
    │
    │── ContainerRemove (cleanup)
    │
    │ (for apply runs: wait for APPLYING state signal)
    │── poll GET /internal/runs/{id} until state=APPLYING or CANCELLED
    │
    │── POST /internal/runs/{id}/events {type: run.applying_started}
    │── DockerExecutor.Execute(apply task)
    │   └── (same log streaming pattern)
    │── POST /internal/runs/{id}/events {type: run.applied, payload: {duration_s: 47}}
    │── POST /internal/workers/{id}/heartbeat {status: IDLE, current_run_id: null}
    │
    │── return to job poll loop
```

---

## Flow 4: Policy Evaluation Gate

```
Scheduler             PolicyService         OPA Evaluator         BundleLoader
    │                    │                    │                    │
    │ run in PLANNED state, checking next state
    │── policyService.Evaluate(EvaluationInput{
    │     run, stack, planOutput, actor
    │   })
    │                    │
    │                    │── loader.GetPoliciesForScope(orgID, spaceID, stackID)
    │                    │◀─ [policy-1: no-public-buckets HARD_FAIL]
    │                    │   [policy-2: require-tags SOFT_WARN]
    │                    │
    │                    │── for each policy:
    │                    │   rego.New(query="data.stratum.policy", input=buildInputDoc())
    │                    │── result = rego.Eval(ctx)
    │                    │
    │                    │   policy-1: deny[] = ["S3 bucket 'data' has public-read ACL"]
    │                    │   enforcement = HARD_FAIL → hardFail = true
    │                    │
    │                    │   policy-2: deny[] = [] (no violations)
    │                    │
    │                    │── return EvaluationResult{
    │                    │     Allow: false,
    │                    │     Severity: HARD_FAIL,
    │                    │     Violations: [{PolicyName: "no-public-buckets", ...}]
    │                    │   }
    │                    │
    │◀── verdict(Allow=false, HARD_FAIL)
    │
    │── runSvc.AppendEvent(policy.evaluated, {verdict, violations})
    │── runSvc.Transition(run.ID, POLICY_REJECTED)
    │── emit outbox: stratum.runs.events.{run-id}
    │
    │ (NATS → WebSocket → UI shows policy rejection with violation details)
    │ (NATS → notification router → Slack: "Run blocked: public S3 bucket")
```

---

## Data Topology: What Lives Where

```
PostgreSQL
├── organizations           IAM context
├── users                   IAM context
├── api_keys                IAM context
├── role_bindings           IAM context
├── spaces                  Stack context
├── stacks                  Stack context
├── stack_dependencies      Stack context
├── stack_variables         Stack context
├── worker_pools            Worker context
├── workers                 Worker context
├── runs                    Run context        ← current_state is derived column
├── run_events              Run context        ← append-only, never updated
├── run_jobs                Run context (scheduler) ← SKIP LOCKED queue
├── run_logs                Run context        ← high-volume, separate
├── policies                Policy context
├── policy_sets             Policy context
├── policy_set_members      Policy context
├── policy_set_bindings     Policy context
├── state_versions          State context
├── state_locks             State context
├── secrets                 Secret context     ← values encrypted at rest
├── secret_claims           Secret context     ← one-time claim tokens
├── reconcile_schedules     Reconcile context
├── drift_records           Reconcile context
├── vcs_connections         VCS context
├── outbox_messages         Platform          ← transactional outbox
└── audit_log               Platform          ← written by NATS archiver

S3-Compatible Object Store (Phase 4+)
├── stratum-state/{org_id}/{stack_id}/{version}/terraform.tfstate
└── stratum-archives/{org_id}/{stack_id}/{commit_sha}.tar.gz

NATS JetStream Streams
├── STRATUM_RUNS      stratum.runs.events.{run_id}
├── STRATUM_STACKS    stratum.stacks.events.{stack_id}
└── STRATUM_AUDIT     stratum.audit.{org_id}
```
