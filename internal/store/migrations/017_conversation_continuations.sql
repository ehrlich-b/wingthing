CREATE TABLE conversation_continuations (
    owner_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    source_session TEXT NOT NULL,
    session_id TEXT NOT NULL UNIQUE REFERENCES conversation_executions(session_id),
    provider_session_id TEXT NOT NULL,
    input_sha256 TEXT NOT NULL,
    spec_digest TEXT NOT NULL,
    prior_launch_state TEXT NOT NULL,
    prior_launch_error TEXT NOT NULL,
    launch_state TEXT NOT NULL DEFAULT 'starting',
    launch_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(owner_id, request_id)
);
