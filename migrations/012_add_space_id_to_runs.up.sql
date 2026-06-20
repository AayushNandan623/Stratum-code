-- 012_add_space_id_to_runs.sql — Add space_id column to runs table.

ALTER TABLE runs ADD COLUMN space_id UUID REFERENCES spaces(id);
CREATE INDEX idx_runs_space_id ON runs(space_id);
