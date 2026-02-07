-- 003_social.sql: Create social tables

-- Social embeddings (the one table)
CREATE TABLE social_embeddings (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    link TEXT,
    text TEXT NOT NULL,
    centerpoint TEXT,
    slug TEXT,
    embedding BLOB NOT NULL,
    embedding_512 BLOB NOT NULL,
    centroid_512 BLOB,
    effective_512 BLOB,
    kind TEXT NOT NULL,  -- 'post', 'subscription', 'anchor', 'antispam'
    visible INTEGER NOT NULL DEFAULT 1,
    mass INTEGER NOT NULL DEFAULT 1,
    upvotes_24h INTEGER NOT NULL DEFAULT 0,
    decayed_mass REAL NOT NULL DEFAULT 1.0,
    swallowed INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_social_link ON social_embeddings(link) WHERE link IS NOT NULL;
CREATE UNIQUE INDEX idx_social_slug ON social_embeddings(slug) WHERE slug IS NOT NULL;
CREATE INDEX idx_social_user ON social_embeddings(user_id);
CREATE INDEX idx_social_kind ON social_embeddings(kind);
CREATE INDEX idx_social_visible ON social_embeddings(visible, kind);
CREATE INDEX idx_social_created ON social_embeddings(created_at);
CREATE INDEX idx_social_mass ON social_embeddings(decayed_mass DESC);

-- Anchor assignments (pre-computed on publish)
CREATE TABLE post_anchors (
    post_id TEXT NOT NULL REFERENCES social_embeddings(id),
    anchor_id TEXT NOT NULL REFERENCES social_embeddings(id),
    similarity REAL NOT NULL,
    PRIMARY KEY (post_id, anchor_id)
);
CREATE INDEX idx_post_anchors_feed ON post_anchors(anchor_id, similarity DESC);

-- Subscriptions
CREATE TABLE social_subscriptions (
    user_id TEXT NOT NULL,
    anchor_id TEXT NOT NULL REFERENCES social_embeddings(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, anchor_id)
);

-- Upvotes
CREATE TABLE social_upvotes (
    user_id TEXT NOT NULL,
    post_id TEXT NOT NULL REFERENCES social_embeddings(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, post_id)
);

-- Comments (threaded)
CREATE TABLE social_comments (
    id TEXT PRIMARY KEY,
    post_id TEXT NOT NULL REFERENCES social_embeddings(id),
    user_id TEXT NOT NULL,
    parent_id TEXT REFERENCES social_comments(id),
    content TEXT NOT NULL,
    is_bot INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_comments_post ON social_comments(post_id, created_at);

-- Social users (OAuth)
CREATE TABLE social_users (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    avatar_url TEXT,
    is_pro INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, provider_id)
);

-- Rate limits (token bucket)
CREATE TABLE social_rate_limits (
    user_id TEXT NOT NULL,
    action TEXT NOT NULL,
    tokens REAL NOT NULL,
    last_refill DATETIME NOT NULL,
    PRIMARY KEY (user_id, action)
);
