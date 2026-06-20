-- 005_init_policy.sql — Policy engine schema.
-- Phase 0 defines schema only; OPA evaluation logic is implemented in Phase 4.

CREATE TABLE policies (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  name        VARCHAR(255) NOT NULL,
  description TEXT,
  rego_text   TEXT NOT NULL,
  enabled     BOOLEAN NOT NULL DEFAULT true,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at  TIMESTAMPTZ,
  UNIQUE (org_id, name)
);

CREATE TABLE policy_bundles (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  name        VARCHAR(255) NOT NULL,
  version     VARCHAR(64) NOT NULL,
  sha256      VARCHAR(64) NOT NULL,
  content     BYTEA NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (org_id, name, version)
);

CREATE TABLE policy_evaluations (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        UUID NOT NULL REFERENCES organizations(id),
  policy_id     UUID REFERENCES policies(id),
  resource_type VARCHAR(32) NOT NULL,
  resource_id   UUID,
  decision      VARCHAR(16) NOT NULL, -- allow | deny
  input         JSONB NOT NULL DEFAULT '{}',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_policy_evaluations_org_id ON policy_evaluations(org_id, created_at);
