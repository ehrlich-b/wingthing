-- Link proofs are single-use and bound to the account that requested them.
CREATE TABLE signin_links (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL,
    used INTEGER NOT NULL DEFAULT 0
);
