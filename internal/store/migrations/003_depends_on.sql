-- 003_depends_on.sql: Add task dependency column
ALTER TABLE tasks ADD COLUMN depends_on TEXT;
