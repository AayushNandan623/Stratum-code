-- 003_init_runs.sql — Run orchestration schema.

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
