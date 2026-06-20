-- 004_init_workers.sql — Worker runtime schema.

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
