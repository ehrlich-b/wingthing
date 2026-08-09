-- 006_task_cwd.sql: Persist the working directory used by prompt tasks.
ALTER TABLE tasks ADD COLUMN cwd TEXT NOT NULL DEFAULT '';
