-- Once an account deliberately removes a method, adding access requires an
-- explicit link proof instead of automatic matching against its stored email.
CREATE TABLE signin_recovery_disabled (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE
);
