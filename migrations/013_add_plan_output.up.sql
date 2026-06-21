-- 013_add_plan_output.sql — Add plan output storage to runs table.

ALTER TABLE runs ADD COLUMN plan_output JSONB;
