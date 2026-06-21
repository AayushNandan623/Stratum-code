-- 015_init_policy_phase4.up.sql
-- Phase 4 additions: enforcement level, policy sets, members, and bindings.
-- Requires 005_init_policy.up.sql (policies table) to have been applied first.

ALTER TABLE policies ADD COLUMN IF NOT EXISTS enforcement VARCHAR(20) NOT NULL DEFAULT 'SOFT_WARN';

CREATE TABLE IF NOT EXISTS policy_sets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL REFERENCES organizations(id),
    name        VARCHAR(255) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE TABLE IF NOT EXISTS policy_set_members (
    policy_set_id UUID NOT NULL REFERENCES policy_sets(id),
    policy_id     UUID NOT NULL REFERENCES policies(id),
    PRIMARY KEY (policy_set_id, policy_id)
);

CREATE TABLE IF NOT EXISTS policy_set_bindings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_set_id   UUID NOT NULL REFERENCES policy_sets(id),
    resource_type   VARCHAR(20) NOT NULL,  -- ORG | SPACE | STACK
    resource_id     UUID NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_policy_set_bindings_resource
    ON policy_set_bindings(resource_type, resource_id);
