CREATE TABLE agent_runs (
    id TEXT PRIMARY KEY REFERENCES tasks(id),
    session_id TEXT NOT NULL UNIQUE,
    principal TEXT NOT NULL,
    request_key TEXT,
    spec_hash TEXT NOT NULL,
    record TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1,
    UNIQUE(principal, request_key)
);
