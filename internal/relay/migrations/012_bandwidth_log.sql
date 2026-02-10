CREATE TABLE IF NOT EXISTS bandwidth_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    month TEXT NOT NULL,
    bytes_total INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_bandwidth_log_user_month ON bandwidth_log(user_id, month);
