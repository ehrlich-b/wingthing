-- A logical conversation outlives a PTY execution and provider reconnect.
CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    root_id TEXT NOT NULL,
    parent_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    agent TEXT NOT NULL,
    cwd TEXT NOT NULL,
    wing_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL UNIQUE,
    launch_key TEXT NOT NULL,
    spec_digest TEXT NOT NULL,
    launch_state TEXT NOT NULL DEFAULT 'starting',
    launch_error TEXT NOT NULL DEFAULT '',
    checkpoint TEXT NOT NULL DEFAULT '',
    delivered_cursor INTEGER NOT NULL DEFAULT 0,
    revision INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_id, launch_key)
);
CREATE INDEX idx_conversations_owner_root ON conversations(owner_id, root_id);
CREATE TABLE conversation_executions (
    session_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    imported_cursor INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE conversation_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    root_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    session_id TEXT NOT NULL,
    source_cursor INTEGER NOT NULL,
    state TEXT NOT NULL,
    state_source TEXT NOT NULL,
    type TEXT NOT NULL DEFAULT 'state',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(session_id, source_cursor, state, state_source)
);
CREATE INDEX idx_conversation_events_root ON conversation_events(root_id, sequence);
