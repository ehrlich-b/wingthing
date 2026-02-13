CREATE TABLE IF NOT EXISTS labels (
    target_id TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    label TEXT NOT NULL,
    PRIMARY KEY(target_id, scope_type, scope_id)
);
