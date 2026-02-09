-- 008_skills_weight.sql: Add source_url and weight to skills

ALTER TABLE skills ADD COLUMN source_url TEXT DEFAULT '';
ALTER TABLE skills ADD COLUMN weight INTEGER DEFAULT 0;
