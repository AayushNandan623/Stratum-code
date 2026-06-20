-- 002_init_stacks.sql — Stack management schema.

CREATE TABLE spaces (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      UUID NOT NULL REFERENCES organizations(id),
  name        VARCHAR(255) NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at  TIMESTAMPTZ
);

CREATE TABLE stacks (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id                UUID NOT NULL REFERENCES organizations(id),
  space_id              UUID REFERENCES spaces(id),
  name                  VARCHAR(255) NOT NULL,
  status                VARCHAR(32) NOT NULL DEFAULT 'ACTIVE',
  vcs_repo              VARCHAR(512),
  vcs_branch            VARCHAR(255) DEFAULT 'main',
  working_dir           VARCHAR(512) DEFAULT '.',
  iac_tool              VARCHAR(32) DEFAULT 'opentofu',
  iac_version           VARCHAR(32) DEFAULT 'latest',
  worker_pool_id        UUID,
  auto_apply            BOOLEAN NOT NULL DEFAULT false,
  reconcile_interval    INTERVAL NOT NULL DEFAULT '1 hour',
  drift_mode            VARCHAR(32) NOT NULL DEFAULT 'NOTIFY',
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at            TIMESTAMPTZ,
  UNIQUE (org_id, name)
);

CREATE TABLE stack_dependencies (
  stack_id        UUID NOT NULL REFERENCES stacks(id),
  depends_on_id   UUID NOT NULL REFERENCES stacks(id),
  PRIMARY KEY (stack_id, depends_on_id)
);

CREATE TABLE stack_variables (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  stack_id    UUID NOT NULL REFERENCES stacks(id),
  key         VARCHAR(255) NOT NULL,
  value       TEXT,
  sensitive   BOOLEAN NOT NULL DEFAULT false,
  category    VARCHAR(20) NOT NULL DEFAULT 'terraform', -- terraform | env
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (stack_id, key)
);
