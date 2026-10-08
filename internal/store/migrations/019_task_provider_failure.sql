-- Store content-free provider failures and exact Codex session references.
ALTER TABLE tasks ADD COLUMN error_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN provider_thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN provider_rollout_path TEXT NOT NULL DEFAULT '';
