CREATE TABLE IF NOT EXISTS anchor_embeddings (
    anchor_id TEXT NOT NULL REFERENCES social_embeddings(id),
    embedder_name TEXT NOT NULL,
    centroid_512 BLOB NOT NULL,
    effective_512 BLOB,
    PRIMARY KEY (anchor_id, embedder_name)
);
