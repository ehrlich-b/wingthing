CREATE TABLE messages (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id      TEXT NOT NULL UNIQUE,
    owner_id        TEXT NOT NULL,
    sender_actor    TEXT NOT NULL,
    recipient_actor TEXT NOT NULL DEFAULT '',
    channel         TEXT NOT NULL,
    kind            TEXT NOT NULL DEFAULT 'message',
    reply_to        TEXT,
    content         TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at      DATETIME NOT NULL,
    FOREIGN KEY(reply_to) REFERENCES messages(message_id) ON DELETE SET NULL
);

CREATE INDEX idx_messages_owner_channel_id
    ON messages(owner_id, channel, id);
CREATE INDEX idx_messages_expiry
    ON messages(expires_at);
