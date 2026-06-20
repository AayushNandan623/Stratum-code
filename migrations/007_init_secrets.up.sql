-- 007_init_secrets.sql — Secret management schema.
-- Phase 0 defines schema only; AES-256-GCM crypto is implemented in Phase 1.

CREATE TABLE secrets (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  scope_type  VARCHAR(20) NOT NULL, -- ORG | SPACE | STACK
  scope_id    UUID NOT NULL,
  key         VARCHAR(255) NOT NULL,
  ciphertext  BYTEA NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at  TIMESTAMPTZ,
  UNIQUE (scope_type, scope_id, key)
);
