-- Keep user IDs (and all ownership/session relationships) stable across providers.
CREATE TABLE user_identities (
    provider TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (provider, provider_id),
    UNIQUE (user_id, provider)
);

INSERT INTO user_identities (provider, provider_id, user_id)
SELECT provider, provider_id, id FROM users;

-- Existing user creation paths continue to register their primary identity.
CREATE TRIGGER users_register_identity AFTER INSERT ON users BEGIN
    INSERT INTO user_identities (provider, provider_id, user_id)
    VALUES (NEW.provider, NEW.provider_id, NEW.id);
END;
