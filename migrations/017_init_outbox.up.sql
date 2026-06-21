-- 017_init_outbox.up.sql
-- Transactional outbox for reliable NATS event delivery.

CREATE TABLE IF NOT EXISTS outbox_messages (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject       VARCHAR(255) NOT NULL,
    payload       JSONB NOT NULL,
    status        VARCHAR(20) NOT NULL DEFAULT 'PENDING',  -- PENDING | IN_FLIGHT | DELIVERED | FAILED
    attempt       INT NOT NULL DEFAULT 0,
    max_attempts  INT NOT NULL DEFAULT 5,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deliver_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_outbox_pending
    ON outbox_messages (deliver_after)
    WHERE status = 'PENDING';
