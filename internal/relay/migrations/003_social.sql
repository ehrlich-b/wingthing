-- 003_social.sql: Create auth user tables
-- (Social feed tables removed; only social_users needed for auth)

CREATE TABLE IF NOT EXISTS social_users (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    avatar_url TEXT,
    is_pro INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, provider_id)
);
