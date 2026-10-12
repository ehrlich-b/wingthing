-- Task table copied exactly from committed migration 001_init.sql.
-- Only fixture construction runs this SQL; inspection never migrates it.
PRAGMA foreign_keys=OFF;
DROP TABLE agent_runs;
DROP TABLE tasks;
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    type        TEXT NOT NULL DEFAULT 'prompt',
    what        TEXT NOT NULL,
    run_at      DATETIME NOT NULL,
    agent       TEXT NOT NULL DEFAULT 'claude',
    isolation   TEXT NOT NULL DEFAULT 'standard',
    memory      TEXT,
    parent_id   TEXT REFERENCES tasks(id),
    status      TEXT NOT NULL DEFAULT 'pending',
    cron        TEXT,
    machine_id  TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at  DATETIME,
    finished_at DATETIME,
    output      TEXT,
    error       TEXT
);
