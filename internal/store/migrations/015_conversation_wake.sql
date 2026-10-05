-- Wake receipt delivery is independent from the parent's explicit checkpoint ACK.
CREATE TABLE conversation_wake_policy (
    root_id TEXT PRIMARY KEY REFERENCES conversations(id),
    enabled INTEGER NOT NULL DEFAULT 0,
    scan_cursor INTEGER NOT NULL DEFAULT 0,
    delivery_cursor INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE conversation_wake_outbox (
    root_id TEXT NOT NULL REFERENCES conversations(id),
    event_sequence INTEGER NOT NULL REFERENCES conversation_events(sequence),
    attempt INTEGER NOT NULL DEFAULT 0,
    request_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    provider_session_id TEXT NOT NULL DEFAULT '',
    input TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'queued',
    receipt_cursor INTEGER NOT NULL DEFAULT 0,
    retry_after INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(root_id,event_sequence)
);
CREATE UNIQUE INDEX conversation_wake_one_pending ON conversation_wake_outbox(root_id) WHERE status <> 'observed';
