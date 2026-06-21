-- 016_init_reconcile_phase5.up.sql
-- Phase 5 additions: reconcile schedules and drift records.
-- Requires 008_init_reconcile.up.sql (drift_reports) to have been applied first.

CREATE TABLE IF NOT EXISTS reconcile_schedules (
    stack_id              UUID PRIMARY KEY REFERENCES stacks(id),
    org_id                UUID NOT NULL REFERENCES organizations(id),
    enabled               BOOLEAN NOT NULL DEFAULT true,
    reconcile_interval    INTERVAL NOT NULL DEFAULT '1 hour',
    drift_mode            VARCHAR(20) NOT NULL DEFAULT 'NOTIFY',
    next_check_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_check_at         TIMESTAMPTZ,
    last_drift_at         TIMESTAMPTZ,
    consecutive_failures  INT NOT NULL DEFAULT 0,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_reconcile_schedule_due
    ON reconcile_schedules (next_check_at)
    WHERE enabled = true;

CREATE TABLE IF NOT EXISTS drift_records (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stack_id              UUID NOT NULL REFERENCES stacks(id),
    org_id                UUID NOT NULL REFERENCES organizations(id),
    trigger_run_id        UUID NOT NULL REFERENCES runs(id),
    status                VARCHAR(20) NOT NULL DEFAULT 'DETECTED',
    resource_count        INT NOT NULL DEFAULT 0,
    drift_summary         JSONB NOT NULL DEFAULT '{}',
    remediation_run_id    UUID REFERENCES runs(id),
    detected_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at           TIMESTAMPTZ,
    ignored_at            TIMESTAMPTZ,
    ignored_by            UUID REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_drift_records_stack_id ON drift_records(stack_id, status);
CREATE INDEX IF NOT EXISTS idx_drift_records_org_id   ON drift_records(org_id, detected_at DESC);
