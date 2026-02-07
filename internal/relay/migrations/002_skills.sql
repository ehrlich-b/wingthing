-- 002_skills.sql: Create skill registry table

CREATE TABLE skills (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    category TEXT NOT NULL,
    agent TEXT DEFAULT '',
    tags TEXT DEFAULT '',
    content TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    publisher TEXT DEFAULT 'wingthing',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_skills_category ON skills(category);
