-- 005_published_at.sql: Add published_at for article original publish date

ALTER TABLE social_embeddings ADD COLUMN published_at DATETIME;

-- Backfill: set published_at = created_at for existing posts
UPDATE social_embeddings SET published_at = created_at WHERE published_at IS NULL;
