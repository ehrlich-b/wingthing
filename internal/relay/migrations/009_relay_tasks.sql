CREATE TABLE IF NOT EXISTS relay_tasks (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL,
    identity    TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    skill       TEXT DEFAULT '',
    agent       TEXT DEFAULT '',
    isolation   TEXT DEFAULT 'standard',
    status      TEXT NOT NULL DEFAULT 'pending',
    output      TEXT DEFAULT '',
    error       TEXT DEFAULT '',
    wing_id     TEXT DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at  DATETIME,
    finished_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_relay_tasks_user_status ON relay_tasks(user_id, status);
CREATE INDEX IF NOT EXISTS idx_relay_tasks_identity ON relay_tasks(identity, status);
