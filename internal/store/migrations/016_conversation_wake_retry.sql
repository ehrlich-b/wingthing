-- Keep attempt identities monotonic when a user authorizes another proven-no-
-- input cycle. This migration also covers an already applied wake outbox.
ALTER TABLE conversation_wake_outbox ADD COLUMN attempt_limit INTEGER NOT NULL DEFAULT 3;
