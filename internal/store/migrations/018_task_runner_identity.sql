-- Bind an agent run's supervising PID to the process lifetime that claimed it.
ALTER TABLE tasks ADD COLUMN runner_identity TEXT NOT NULL DEFAULT '';
