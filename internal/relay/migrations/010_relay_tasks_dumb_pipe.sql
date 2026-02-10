-- Rebuild relay_tasks as dumb pipe: opaque payload, no task content.
-- Old table stored prompt/output/error/skill/agent/isolation — all boundary violations.
-- New table stores only routing metadata + opaque JSON blob forwarded to wing as-is.
DROP TABLE IF EXISTS relay_tasks;
CREATE TABLE relay_tasks (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    identity   TEXT NOT NULL DEFAULT '',
    payload    TEXT NOT NULL DEFAULT '',
    wing_id    TEXT DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_relay_tasks_user_status ON relay_tasks(user_id, status);
