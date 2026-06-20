# Developer Onboarding

## Overview

This guide takes a new developer from zero to productive on Stratum in a structured sequence. Follow each section in order.

---

## Step 1: Understand What Stratum Does (30 min)

Read these in order:
1. `README.md` — Project overview and key concepts
2. `docs/architecture/00-system-overview.md` — What it is, why the stack was chosen

After reading, you should be able to answer:
- What is a Stack? What is a Run? What is a Worker?
- What does "desired-state reconciliation" mean in this context?
- Why is PostgreSQL used as a job queue in early phases?

---

## Step 2: Understand System Decomposition (45 min)

Read:
3. `docs/architecture/01-bounded-contexts.md` — The 9 contexts and their boundaries

After reading, you should be able to answer:
- Which context owns the run event store?
- Can the Run context query the Policy database tables directly?
- What does the VCS context do when it receives a webhook?

---

## Step 3: Understand the Core Lifecycle (60 min)

Read:
4. `docs/architecture/02-execution-model.md` — Run state machine, idempotency, retry
5. `docs/architecture/03-orchestration-model.md` — Scheduler, DAG, job queue
6. `docs/architecture/04-worker-model.md` — Worker protocol, Docker execution

After reading, you should be able to trace through this scenario entirely:
> A developer pushes to `main` on a GitHub repository. A VCS webhook arrives. Trace the full flow from webhook receipt to infrastructure being applied and the run completing.

If you can't trace it, re-read sections 4-6.

---

## Step 4: Understand the Cross-Cutting Concerns (45 min)

Read:
7. `docs/architecture/05-event-model.md` — Event sourcing, outbox, NATS
8. `docs/architecture/06-reconciliation.md` — Drift detection controller
9. `docs/architecture/07-security-model.md` — RBAC, secrets, policy
10. `docs/architecture/08-scaling-failure.md` — Failures, retry taxonomy, scaling path

---

## Step 5: Understand Design Decisions (20 min)

Read:
11. `docs/adr/README.md` — All ADRs

Focus on: ADR-002 (why PostgreSQL queue), ADR-003 (why event sourcing is scoped), ADR-009 (why Temporal is deferred). These explain the non-obvious choices.

---

## Step 6: Set Up Local Environment

```bash
# Prerequisites check
go version      # need 1.22+
docker version  # need 24+
make --version  # need any recent version

# Setup
git clone <repo> && cd stratum
make dev-setup

# Verify
curl http://localhost:8080/healthz
# Expected: {"status":"ok"}
```

**If `make dev-setup` fails:**
- Check Docker is running: `docker ps`
- Check port 5432 is free: `lsof -i :5432`
- Check port 4222 is free: `lsof -i :4222` (NATS, Phase 6+)

---

## Step 7: Explore the Schema (30 min)

Connect to the local database and explore:

```bash
psql postgresql://stratum:stratum@localhost:5432/stratum

-- See all tables
\dt

-- Understand the run lifecycle tables
\d runs
\d run_events
\d run_jobs

-- See a run's event sequence
SELECT seq, event_type, actor_type, occurred_at, payload
FROM run_events
WHERE run_id = '<some-run-id>'
ORDER BY seq;
```

Understand what each table owns before touching any code.

---

## Step 8: Read the Repository Structure (15 min)

Read `REPOSITORY.md`. Pay close attention to:
- The import rules section (what can import what)
- The module ownership map (which phase introduces which module)
- The binary responsibilities (what each binary does)

---

## Step 9: Read the Implementation Docs (by phase)

Before implementing any phase, read its phase document:

```
docs/impl/phases/phase-0-foundation.md
docs/impl/phases/phase-1-stack-management.md
docs/impl/phases/phase-2-run-orchestration.md
docs/impl/phases/phase-3-worker-runtime.md
docs/impl/phases/phase-4-policy-engine.md
docs/impl/phases/phase-5-reconciliation.md
docs/impl/phases/phase-6-event-sourcing.md
```

Also bookmark `docs/impl/modules/interfaces.md` — the canonical interface reference. When you're not sure what a service interface looks like, this is the answer.

---

## Key Mental Models

### The Reconciliation Loop
Stratum's core is a reconciliation loop, exactly like Kubernetes controllers. The mental model is:

```
desired state  →  observe actual state  →  compute diff  →  act to converge  →  repeat
(IaC config)     (Terraform state)         (plan output)    (apply run)
```

Everything in Stratum serves this loop: stacks define desired state, runs compute and apply diffs, workers execute the computation, the reconciler continuously re-evaluates.

### Events are Immutable
Run events are never updated or deleted. If you find yourself wanting to update an event, you're doing it wrong. Corrections are new events that supersede earlier ones. The current state is always the last event in the sequence.

### Context Isolation is Enforced at the Import Level
If you find yourself importing `internal/run/` from `internal/stack/`, stop. Read the import rules again. All cross-context communication goes through service interfaces defined in each context's `service.go`.

### Workers are Dumb by Design
Workers contain no business logic. They: claim jobs, execute containers, stream logs, report results. All decisions (should this run proceed? is there a policy violation? should this be re-queued?) happen in the control plane. If a worker is making orchestration decisions, something is wrong.

---

## Common Mistakes to Avoid

| Mistake | Why It's Wrong | Correct Approach |
|---------|---------------|-----------------|
| Writing run state directly to `runs.current_state` | Bypasses event store | Use `RunService.Transition()` which writes an event AND updates state atomically |
| Querying another context's tables | Breaks bounded context isolation | Call the other context's service interface |
| Adding business logic to repository files | Repository = SQL only | Put logic in service.go |
| Storing secret values in logs | Security violation | Check logs for `scrubSensitive()` call before storage |
| Retrying a run after mid-apply crash | May duplicate infrastructure | Mid-apply failures require human review — mark FAILED |
| Using `fmt.Println` for logging | Not structured, not searchable | Use `slog.Info()` / `slog.Error()` with key-value fields |
| Importing `internal/api/` from domain packages | Inverted dependency | API layer depends on domain, never the reverse |

---

## Asking Questions About the Architecture

Before asking why something is designed a certain way, check:
1. `docs/adr/README.md` — There's probably an ADR for it
2. The phase document for when the code was introduced
3. The bounded context doc for ownership questions

If the ADR doesn't exist and the design seems questionable, it may be a good candidate for a new ADR to document the decision retroactively.

---

## First Contribution Checklist

Before your first PR:
- [ ] `make build` passes
- [ ] `make test` passes (or failures are pre-existing and documented)
- [ ] `make lint` passes
- [ ] New DB tables have a migration file in `migrations/`
- [ ] New service methods have entries in the relevant `service.go` interface
- [ ] No cross-context DB queries (repository files only query their own tables)
- [ ] All log lines use structured fields, not string formatting
- [ ] Sensitive values (secrets, tokens) are not logged
- [ ] New API endpoints have RBAC middleware applied
- [ ] New domain errors are typed `DomainError`, not `fmt.Errorf` plain strings

---

## Architecture Office Hours

For design questions that aren't answered by documentation, prefer:
1. Opening a GitHub issue with the `architecture-question` label
2. Writing a mini-RFC if the question implies a design change
3. Adding to the ADR if a decision gets made

Decisions made in Slack or verbal discussions that affect the codebase should be documented as ADRs within a week.
