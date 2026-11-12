-- Proof belongs to a provider subject, independently of the mutable primary
-- provider and unique contact email. Legacy rows have no recorded proof: do not
-- infer verified addresses or recovery provenance from opaque OAuth subjects.
CREATE UNIQUE INDEX user_identity_binding ON user_identities(provider, provider_id, user_id);

CREATE TABLE signin_identity_proofs (
    provider TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provenance TEXT NOT NULL CHECK (provenance IN ('explicit_link', 'verified_recovery', 'verified_signup')),
    verified_email TEXT NOT NULL,
    proved_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (provider, provider_id),
    FOREIGN KEY (provider, provider_id, user_id) REFERENCES user_identities(provider, provider_id, user_id) ON DELETE CASCADE
);

-- Latest verified provider enrollment is authorization state, not ownership of
-- a contact/recovery address. NULL explicitly revokes stale authorization.
CREATE TABLE user_oauth_enrollment (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    verified_email TEXT,
    checked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
