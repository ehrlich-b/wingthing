-- 001_init.sql: Create all core tables for wingthing v0.1

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

CREATE INDEX idx_tasks_status_run_at ON tasks(status, run_at);

CREATE TABLE thread_entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT REFERENCES tasks(id),
    timestamp   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    machine_id  TEXT NOT NULL,
    agent       TEXT,
    skill       TEXT,
    user_input  TEXT,
    summary     TEXT NOT NULL,
    tokens_used INTEGER
);

CREATE INDEX idx_thread_timestamp ON thread_entries(timestamp);

CREATE TABLE agents (
    name            TEXT PRIMARY KEY,
    adapter         TEXT NOT NULL,
    command         TEXT NOT NULL,
    context_window  INTEGER NOT NULL DEFAULT 200000,
    default_isolation TEXT DEFAULT 'standard',
    healthy         BOOLEAN DEFAULT 0,
    health_checked  DATETIME,
    config_json     TEXT
);

CREATE TABLE task_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT REFERENCES tasks(id),
    timestamp   DATETIME DEFAULT CURRENT_TIMESTAMP,
    event       TEXT NOT NULL,
    detail      TEXT
);
