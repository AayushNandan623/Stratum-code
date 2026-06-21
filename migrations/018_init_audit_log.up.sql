-- 018_init_audit_log.up.sql
-- Immutable audit log populated by the NATS audit archiver consumer.

CREATE TABLE IF NOT EXISTS audit_log (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        UUID NOT NULL,
    actor_id      UUID,
    actor_type    VARCHAR(20) NOT NULL,  -- USER | API_KEY | WORKER | SYSTEM
    action        VARCHAR(128) NOT NULL, -- "run.applied", "stack.created", "policy.evaluated", etc.
    resource_type VARCHAR(64) NOT NULL,
    resource_id   UUID,
    metadata      JSONB NOT NULL DEFAULT '{}',
    ip_address    INET,
    occurred_at   TIMESTAMPTZ NOT NULL,
    inserted_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_log_org_time
    ON audit_log (org_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_resource
    ON audit_log (resource_type, resource_id, occurred_at DESC);
