# Context Optimization Strategy

## The Problem

AI coding agents have finite context windows. A poorly-structured codebase forces agents to load large amounts of irrelevant context to answer simple questions. This leads to:
- Increased token usage per task
- Hallucinated implementations that ignore existing patterns
- Cross-context boundary violations
- Inconsistent interfaces

Stratum's documentation and code structure are explicitly designed to prevent this.

---

## Principle 1: One File, One Concern

Every file in `internal/` has a single, clearly-named responsibility:

```
repository.go   → database queries only. No business logic. No HTTP.
service.go      → business logic only. No SQL. No HTTP.
types.go        → domain types only. No methods with side effects.
handlers.go     → HTTP only. No business logic.
```

An agent implementing `internal/run/repository.go` needs only:
- The DB schema for runs (from `phase-0-foundation.md`)
- The `Run` and `RunEvent` types from `internal/run/types.go`
- `internal/platform/db/db.go` for the DBTX interface

It does NOT need architecture docs, other context packages, or the API layer.

---

## Principle 2: Phase Documents Are Self-Contained

Each phase document contains everything needed to implement that phase:
- DB schema (SQL)
- Interface definitions (Go)
- API endpoints (HTTP verbs + paths)
- Key algorithms (pseudocode, not full implementation)
- Validation criteria (how to verify it works)

An agent implementing Phase 2 should load exactly:
```
docs/impl/README.md                          (orientation, once)
docs/impl/phases/phase-2-run-orchestration.md  (the task)
REPOSITORY.md                                (file paths)
internal/run/types.go                        (domain types, if exists)
internal/platform/db/db.go                   (shared infrastructure)
```

Total context: ~400-600 lines. Not 10,000.

---

## Principle 3: Interfaces as Contracts

When an agent implements context A and needs to call context B, it uses B's service interface — defined in `docs/impl/modules/interfaces.md`. The agent does NOT need to read B's implementation.

This means:
- Implementing the policy engine (Phase 4) does NOT require reading the run scheduler code
- Implementing the reconciler (Phase 5) does NOT require reading the worker code
- The interfaces file is the single source of truth for cross-context contracts

---

## Principle 4: File Size Budget

No file exceeds 400 lines. This is a hard constraint for context window reasons.

For reference:
- 400 lines of Go ≈ 6,000-8,000 tokens
- A 4k context window agent can load 1-2 files comfortably
- A 32k context window agent can load ~5 files at full resolution

Files approaching 400 lines should be split by:
- `repository_runs.go` + `repository_events.go` (split repository by entity)
- `service_lifecycle.go` + `service_events.go` (split service by responsibility)
- `handler_runs.go` + `handler_approvals.go` (split handlers by resource)

---

## Principle 5: No God Documents

Avoid documents that try to explain everything. The `docs/architecture/` tree is structured so each file covers exactly one architectural concern:
- `01-bounded-contexts.md` — isolation and ownership
- `02-execution-model.md` — state machines
- `03-orchestration-model.md` — scheduling
- etc.

An agent debugging a scheduling issue loads `03-orchestration-model.md`. It does not need to load the security model or reconciliation docs.

---

## Recommended Agent Prompt Patterns

### Pattern A: Implementing a New Phase

```
Load these files in order:
1. docs/impl/README.md
2. docs/impl/phases/phase-N-<name>.md
3. REPOSITORY.md

Then implement:
- The DB migration file(s) specified
- internal/<context>/types.go
- internal/<context>/repository.go
- internal/<context>/service.go
- internal/api/handlers/<context>.go

After each file: verify it compiles. Do not implement multiple files without compiling.
```

### Pattern B: Adding a Feature to an Existing Context

```
Load these files:
1. internal/<context>/types.go      (existing domain model)
2. internal/<context>/service.go    (existing service interface)
3. internal/<context>/repository.go (existing queries)
4. docs/impl/phases/phase-N-*.md    (phase that introduced this context)

Then:
- Add new types to types.go
- Add new method to service interface in service.go
- Add new query to repository.go
- Add new handler to internal/api/handlers/<context>.go
- Add migration if schema changes
```

### Pattern C: Debugging a Cross-Context Flow

```
Load these files:
1. docs/architecture/09-sequence-diagrams.md  (find the relevant flow)
2. docs/architecture/01-bounded-contexts.md   (verify ownership)
3. The specific service interfaces for involved contexts
   (from docs/impl/modules/interfaces.md)

Do NOT load implementation files until the flow is understood.
```

### Pattern D: Understanding a Design Decision

```
Load only:
1. docs/adr/README.md (find the relevant ADR number)

If not in ADRs: check docs/architecture/<relevant-file>.md
```

---

## Anti-Patterns (What NOT to Do)

❌ **Loading the entire docs/ tree** — almost never necessary. Pick the specific doc.

❌ **Loading service.go + repository.go + handlers.go simultaneously** — implement one at a time. Start with types.go, then service interface, then implementation, then handler.

❌ **Reading implementation to understand interfaces** — read interfaces.md instead.

❌ **Loading Phase 4 docs while implementing Phase 2** — strictly forbidden. Only the current phase document is needed.

❌ **Asking "what does the whole system do?" before a focused task** — read the README and system overview once, then immediately narrow to the task.

---

## Documentation File Size Reference

| File | Approximate Lines | Approximate Tokens | Load For |
|------|------------------|-------------------|---------|
| `README.md` | 60 | ~900 | Initial orientation |
| `REPOSITORY.md` | 120 | ~1,800 | File paths, imports |
| `docs/impl/README.md` | 80 | ~1,200 | Agent orientation |
| `docs/impl/phases/phase-N-*.md` | 150-250 | ~2,500-4,000 | Phase implementation |
| `docs/impl/modules/interfaces.md` | 220 | ~3,500 | Cross-context contracts |
| `docs/architecture/00-*.md` | 110 | ~1,700 | System understanding |
| `docs/architecture/01-*.md` | 130 | ~2,000 | Context ownership |
| `docs/architecture/02-*.md` | 120 | ~1,800 | State machines |
| `docs/architecture/09-*.md` | 200 | ~3,000 | Flow debugging |
| `docs/adr/README.md` | 150 | ~2,200 | Decision rationale |

A typical agent task (implement one module) requires: ~6,000-10,000 tokens of documentation context.
This leaves ample context for the actual code being written (typical Go service file: ~200 lines = ~3,000 tokens).

---

## Evolution of This Strategy

As Stratum grows, maintain these invariants:
- New bounded contexts get a new directory + a new phase document
- Interfaces.md is updated when any service interface changes
- No file exceeds 400 lines (split proactively, not reactively)
- ADRs are written when design decisions are made, not discovered later
- Phase documents are never retroactively modified — new phases = new documents
