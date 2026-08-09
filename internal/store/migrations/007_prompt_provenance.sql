-- 007_prompt_provenance.sql: Link task runs to immutable prompt revisions.
ALTER TABLE tasks ADD COLUMN prompt_name TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN prompt_revision TEXT NOT NULL DEFAULT '';
