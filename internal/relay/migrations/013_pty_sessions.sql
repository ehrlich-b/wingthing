CREATE TABLE IF NOT EXISTS pty_sessions (
    id TEXT PRIMARY KEY,
    wing_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    agent TEXT NOT NULL DEFAULT '',
    cwd TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'detached',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
