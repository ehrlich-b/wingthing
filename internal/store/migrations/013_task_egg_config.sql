-- Freeze the resolved egg policy with a task so queued and retried runs use
-- the same reviewed sandbox configuration as the submitting invocation.
ALTER TABLE tasks ADD COLUMN egg_config TEXT NOT NULL DEFAULT '';
