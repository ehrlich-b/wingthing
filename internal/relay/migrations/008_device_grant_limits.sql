ALTER TABLE device_codes ADD COLUMN source_ip TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_device_codes_pending ON device_codes(claimed, expires_at);
CREATE INDEX idx_device_codes_source_ip ON device_codes(source_ip, claimed, expires_at);
