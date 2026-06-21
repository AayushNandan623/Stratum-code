-- 014_add_worker_token_hash.down.sql — Remove token_hash column from workers table.

DROP INDEX IF EXISTS idx_workers_token_hash;
ALTER TABLE workers DROP COLUMN IF EXISTS token_hash;
