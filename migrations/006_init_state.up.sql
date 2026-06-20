-- 006_init_state.sql — Remote state schema.
-- Phase 0 defines schema only; state storage backends are implemented in Phase 1.

CREATE TABLE state_versions (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  stack_id    UUID NOT NULL REFERENCES stacks(id),
  serial      INT NOT NULL,
  sha256      VARCHAR(64) NOT NULL,
  size_bytes  BIGINT NOT NULL DEFAULT 0,
  storage_uri VARCHAR(1024) NOT NULL,
  created_by  UUID,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (stack_id, serial)
);

CREATE INDEX idx_state_versions_stack_id ON state_versions(stack_id, serial);
