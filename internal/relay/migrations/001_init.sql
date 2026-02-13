-- 001_init.sql: Complete relay server schema

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    avatar_url TEXT,
    email TEXT,
    tier TEXT NOT NULL DEFAULT 'free',
    is_pro INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, provider_id)
);
CREATE UNIQUE INDEX idx_users_email ON users(email) WHERE email IS NOT NULL;

CREATE TABLE device_tokens (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    device_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME
);

CREATE TABLE device_codes (
    code TEXT PRIMARY KEY,
    user_code TEXT NOT NULL,
    user_id TEXT,
    device_id TEXT NOT NULL,
    public_key TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    claimed BOOLEAN DEFAULT 0
);

CREATE TABLE audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    user_id TEXT,
    event TEXT NOT NULL,
    detail TEXT
);

CREATE TABLE skills (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    category TEXT NOT NULL,
    agent TEXT DEFAULT '',
    tags TEXT DEFAULT '',
    content TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    publisher TEXT DEFAULT 'wingthing',
    source_url TEXT DEFAULT '',
    weight INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_skills_category ON skills(category);

CREATE TABLE sessions (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE magic_links (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    token TEXT UNIQUE NOT NULL,
    expires_at DATETIME NOT NULL,
    used INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE relay_config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE relay_tasks (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    identity TEXT NOT NULL DEFAULT '',
    payload TEXT NOT NULL DEFAULT '',
    wing_id TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_relay_tasks_user_status ON relay_tasks(user_id, status);

CREATE TABLE bandwidth_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id TEXT NOT NULL,
    month TEXT NOT NULL,
    bytes_total INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX idx_bandwidth_log_user_month ON bandwidth_log(user_id, month);

CREATE TABLE orgs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT UNIQUE NOT NULL,
    owner_user_id TEXT NOT NULL,
    max_seats INTEGER NOT NULL DEFAULT 5,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE org_members (
    org_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'member',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, user_id)
);

CREATE TABLE org_invites (
    id TEXT PRIMARY KEY,
    org_id TEXT NOT NULL,
    email TEXT NOT NULL,
    token TEXT UNIQUE NOT NULL,
    invited_by TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    claimed_at DATETIME,
    UNIQUE(org_id, email)
);

CREATE TABLE subscriptions (
    id TEXT PRIMARY KEY,
    user_id TEXT,
    org_id TEXT,
    plan TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    seats INTEGER NOT NULL DEFAULT 1,
    stripe_subscription_id TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE entitlements (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    subscription_id TEXT NOT NULL REFERENCES subscriptions(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, subscription_id)
);
CREATE INDEX idx_entitlements_user ON entitlements(user_id);
CREATE INDEX idx_entitlements_sub ON entitlements(subscription_id);
CREATE INDEX idx_subscriptions_org ON subscriptions(org_id);
CREATE INDEX idx_subscriptions_user ON subscriptions(user_id);
