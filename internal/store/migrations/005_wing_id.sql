-- 005_wing_id.sql: Rename machine_id to wing_id
ALTER TABLE tasks RENAME COLUMN machine_id TO wing_id;
ALTER TABLE thread_entries RENAME COLUMN machine_id TO wing_id;
