-- 011_init_run_logs.sql — Run log lines storage.

CREATE TABLE run_logs (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id      UUID NOT NULL REFERENCES runs(id),
  seq         BIGINT NOT NULL,
  line        TEXT NOT NULL,
  source      VARCHAR(16) NOT NULL DEFAULT 'stdout',  -- stdout | stderr
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  inserted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_run_logs_run_id ON run_logs(run_id, seq);
