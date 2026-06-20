# Phase 0: Foundation

## Scope

**IN scope:**
- PostgreSQL connection pool (`internal/platform/db/`)
- Configuration loading from environment (`internal/platform/config/`)
- Structured logger setup (`internal/platform/logger/`)
- OpenTelemetry skeleton (`internal/platform/telemetry/`)
- Clock abstraction (`internal/platform/clock/`)
- HTTP server scaffolding (`internal/api/server.go`)
- Health check endpoint (`GET /healthz`)
- Migration runner (using `golang-migrate/migrate`)
- Base migrations for ALL contexts (schema only, no logic)
- Shared domain error type (`internal/platform/errors/`)

**OUT of scope:**
- Any business logic
- Any domain-specific handlers
- Authentication (Phase 1)
- Any bounded context implementation

---

## Prerequisites

None. This is the foundation.

---

## Files to Create

```
internal/platform/
  config/config.go          env-var config loading (no viper, use os.Getenv)
  db/db.go                  pgxpool connection, transaction helpers
  db/migrate.go             migration runner wrapper
  logger/logger.go          slog-based structured logger
  telemetry/otel.go         OpenTelemetry provider init (noop initially)
  clock/clock.go            Clock interface + real/fake implementations
  errors/errors.go          DomainError type, standard error codes

internal/api/
  server.go                 HTTP server struct, Start(), Shutdown()
  router.go                 Route registration, health endpoint
  middleware/request_id.go  X-Request-ID injection

cmd/stratum-server/
  main.go                   Wire platform components, start server

migrations/
  001_init_iam.sql
  002_init_stacks.sql
  003_init_runs.sql
  004_init_workers.sql
  005_init_policy.sql
  006_init_state.sql
  007_init_secrets.sql
  008_init_reconcile.sql
  009_init_events.sql
  010_init_vcs.sql

Makefile                    build, migrate, dev targets
deploy/docker-compose.yml   postgres + nats services for local dev
```

---

## Environment Variables

```
STRATUM_DB_URL             postgresql://user:pass@host:5432/stratum?sslmode=disable
STRATUM_HTTP_PORT          8080
STRATUM_LOG_LEVEL          info|debug|warn|error
STRATUM_ENV                development|production
STRATUM_ENCRYPTION_KEY     32-byte hex (for secrets; fake value OK in dev)
```

---

## DB Schema — All Tables (Phase 0 defines schema; contexts implement logic later)

### 001_init_iam.sql
```sql
CREATE TABLE organizations (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        VARCHAR(255) NOT NULL,
  slug        VARCHAR(63) NOT NULL UNIQUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at  TIMESTAMPTZ
);

CREATE TABLE users (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          UUID NOT NULL REFERENCES organizations(id),
  email           VARCHAR(255) NOT NULL,
  password_hash   VARCHAR(255),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at      TIMESTAMPTZ,
  UNIQUE (org_id, email)
);

CREATE TABLE api_keys (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  user_id     UUID REFERENCES users(id),
  name        VARCHAR(255) NOT NULL,
  key_hash    VARCHAR(64) NOT NULL UNIQUE,
  scopes      TEXT[] NOT NULL DEFAULT '{}',
  expires_at  TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_bindings (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        UUID NOT NULL REFERENCES organizations(id),
  subject_type  VARCHAR(20) NOT NULL, -- USER | API_KEY
  subject_id    UUID NOT NULL,
  role          VARCHAR(64) NOT NULL,
  resource_type VARCHAR(20),          -- ORG | SPACE | STACK
  resource_id   UUID,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 002_init_stacks.sql
```sql
CREATE TABLE spaces (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  name        VARCHAR(255) NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at  TIMESTAMPTZ
);

CREATE TABLE stacks (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id                UUID NOT NULL REFERENCES organizations(id),
  space_id              UUID REFERENCES spaces(id),
  name                  VARCHAR(255) NOT NULL,
  status                VARCHAR(32) NOT NULL DEFAULT 'ACTIVE',
  vcs_repo              VARCHAR(512),
  vcs_branch            VARCHAR(255) DEFAULT 'main',
  working_dir           VARCHAR(512) DEFAULT '.',
  iac_tool              VARCHAR(32) DEFAULT 'opentofu',
  iac_version           VARCHAR(32) DEFAULT 'latest',
  worker_pool_id        UUID,
  auto_apply            BOOLEAN NOT NULL DEFAULT false,
  reconcile_interval    INTERVAL NOT NULL DEFAULT '1 hour',
  drift_mode            VARCHAR(32) NOT NULL DEFAULT 'NOTIFY',
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at            TIMESTAMPTZ,
  UNIQUE (org_id, name)
);

CREATE TABLE stack_dependencies (
  stack_id        UUID NOT NULL REFERENCES stacks(id),
  depends_on_id   UUID NOT NULL REFERENCES stacks(id),
  PRIMARY KEY (stack_id, depends_on_id)
);

CREATE TABLE stack_variables (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  stack_id    UUID NOT NULL REFERENCES stacks(id),
  key         VARCHAR(255) NOT NULL,
  value       TEXT,
  sensitive   BOOLEAN NOT NULL DEFAULT false,
  category    VARCHAR(20) NOT NULL DEFAULT 'terraform', -- terraform | env
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (stack_id, key)
);
```

### 003_init_runs.sql
```sql
CREATE TABLE runs (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          UUID NOT NULL REFERENCES organizations(id),
  stack_id        UUID NOT NULL REFERENCES stacks(id),
  run_type        VARCHAR(20) NOT NULL, -- plan | apply | destroy | drift_detect
  current_state   VARCHAR(32) NOT NULL DEFAULT 'PENDING',
  trigger_type    VARCHAR(32) NOT NULL, -- manual | vcs_push | schedule | drift
  triggered_by    UUID,                  -- user_id or null for system
  config_version  VARCHAR(64),           -- git commit SHA at trigger time
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_runs_stack_id ON runs(stack_id);
CREATE INDEX idx_runs_org_id_state ON runs(org_id, current_state);

CREATE TABLE run_events (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id      UUID NOT NULL REFERENCES runs(id),
  org_id      UUID NOT NULL,
  seq         BIGINT NOT NULL,
  event_type  VARCHAR(64) NOT NULL,
  actor_id    UUID,
  actor_type  VARCHAR(20),
  payload     JSONB NOT NULL DEFAULT '{}',
  occurred_at TIMESTAMPTZ NOT NULL,
  inserted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (run_id, seq)
);

CREATE INDEX idx_run_events_run_id ON run_events(run_id, seq);

CREATE TABLE run_jobs (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id      UUID NOT NULL REFERENCES runs(id),
  pool_id     UUID,
  status      VARCHAR(20) NOT NULL DEFAULT 'AVAILABLE',
  claimed_by  UUID,
  claimed_at  TIMESTAMPTZ,
  expires_at  TIMESTAMPTZ NOT NULL DEFAULT now() + interval '60 seconds',
  attempt     INT NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_run_jobs_status ON run_jobs(status, created_at) WHERE status = 'AVAILABLE';
```

### 004_init_workers.sql
```sql
CREATE TABLE worker_pools (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          UUID NOT NULL REFERENCES organizations(id),
  name            VARCHAR(255) NOT NULL,
  pool_type       VARCHAR(20) NOT NULL DEFAULT 'PRIVATE', -- HOSTED | PRIVATE
  token_hash      VARCHAR(64) NOT NULL UNIQUE,
  max_concurrency INT NOT NULL DEFAULT 5,
  labels          JSONB NOT NULL DEFAULT '{}',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at      TIMESTAMPTZ
);

CREATE TABLE workers (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  pool_id         UUID NOT NULL REFERENCES worker_pools(id),
  org_id          UUID NOT NULL,
  hostname        VARCHAR(255),
  version         VARCHAR(32),
  capabilities    TEXT[] NOT NULL DEFAULT '{}',
  status          VARCHAR(20) NOT NULL DEFAULT 'IDLE', -- IDLE | RUNNING | DISCONNECTED
  last_heartbeat  TIMESTAMPTZ,
  current_run_id  UUID,
  registered_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### Remaining migrations (005-010): Define schemas for policy, state, secrets, reconcile, events outbox, VCS in same pattern. Content is in respective bounded context architecture docs.

---

## Interfaces Defined in Phase 0

```go
// internal/platform/clock/clock.go
type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
}

// internal/platform/db/db.go
type DBTX interface {
    Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// internal/platform/errors/errors.go
type DomainError struct {
    Code       string
    Message    string
    HTTPStatus int
}
```

---

## Validation Criteria

After Phase 0 implementation:
1. `make build` compiles without error
2. `make migrate` applies all 10 migrations cleanly
3. `curl http://localhost:8080/healthz` returns `{"status":"ok"}`
4. Structured JSON logs appear on stdout
5. PostgreSQL tables exist and match schema above
