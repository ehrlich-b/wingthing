-- 013_orgs.sql: Add email/tier to social_users, create org tables

-- Add email and tier columns to social_users
ALTER TABLE social_users ADD COLUMN email TEXT;
ALTER TABLE social_users ADD COLUMN tier TEXT NOT NULL DEFAULT 'free';
CREATE UNIQUE INDEX IF NOT EXISTS idx_social_users_email ON social_users(email) WHERE email IS NOT NULL;

-- Add public_key to device_codes (needed for newer flows, may already exist)
-- ALTER TABLE device_codes ADD COLUMN public_key TEXT;

CREATE TABLE IF NOT EXISTS orgs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT UNIQUE NOT NULL,
    owner_user_id TEXT NOT NULL,
    max_seats INTEGER NOT NULL DEFAULT 5,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS org_members (
    org_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'member',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, user_id)
);

CREATE TABLE IF NOT EXISTS org_invites (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL,
    email TEXT NOT NULL,
    token TEXT UNIQUE NOT NULL,
    invited_by TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    claimed_at DATETIME,
    UNIQUE(org_id, email)
);
