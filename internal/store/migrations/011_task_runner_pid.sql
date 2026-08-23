-- 011_task_runner_pid.sql: identify the process supervising a submitted agent run.

ALTER TABLE tasks ADD COLUMN runner_pid INTEGER NOT NULL DEFAULT 0;
