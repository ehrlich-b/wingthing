-- JWT signing key and relay config
CREATE TABLE IF NOT EXISTS relay_config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Add public_key to device_codes so it travels through the claim flow
ALTER TABLE device_codes ADD COLUMN public_key TEXT;
