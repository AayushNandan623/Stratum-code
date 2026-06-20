-- 010_init_vcs.sql — VCS integration schema.
-- Phase 0 defines schema only; provider clients are implemented in Phase 1.

CREATE TABLE vcs_connections (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        UUID NOT NULL REFERENCES organizations(id),
  provider      VARCHAR(32) NOT NULL, -- github | gitlab | bitbucket
  name          VARCHAR(255) NOT NULL,
  base_url      VARCHAR(512),
  api_token_ref UUID REFERENCES secrets(id),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at    TIMESTAMPTZ,
  UNIQUE (org_id, name)
);

CREATE TABLE webhook_events (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        UUID NOT NULL REFERENCES organizations(id),
  connection_id UUID NOT NULL REFERENCES vcs_connections(id),
  event_type    VARCHAR(64) NOT NULL,
  payload       JSONB NOT NULL DEFAULT '{}',
  received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  processed_at  TIMESTAMPTZ
);

CREATE INDEX idx_webhook_events_connection_id ON webhook_events(connection_id, received_at);
