-- 014_add_worker_token_hash.sql — Add token_hash column to workers table
-- for HMAC-based worker authentication (WorkerAuth middleware).

ALTER TABLE workers ADD COLUMN token_hash VARCHAR(64) NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_workers_token_hash ON workers(token_hash) WHERE token_hash <> '';
