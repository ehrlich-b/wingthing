-- Drop UNIQUE constraint on orgs.slug (allow duplicate org names across users).
-- SQLite doesn't support ALTER TABLE DROP CONSTRAINT, so recreate the table.
CREATE TABLE orgs_new (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    max_seats INTEGER NOT NULL DEFAULT 5,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO orgs_new SELECT * FROM orgs;
DROP TABLE orgs;
ALTER TABLE orgs_new RENAME TO orgs;
