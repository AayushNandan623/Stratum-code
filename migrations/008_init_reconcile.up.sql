-- 008_init_reconcile.sql — Drift detection schema.
-- Phase 0 defines schema only; the reconciler loop is implemented in Phase 5.

CREATE TABLE drift_reports (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  stack_id    UUID NOT NULL REFERENCES stacks(id),
  run_id      UUID REFERENCES runs(id),
  status      VARCHAR(32) NOT NULL DEFAULT 'PENDING', -- PENDING | DRIFTED | IN_SYNC | ERROR
  summary     JSONB NOT NULL DEFAULT '{}',
  detected_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_drift_reports_stack_id ON drift_reports(stack_id, created_at);
