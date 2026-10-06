ALTER TABLE mcp_oauth_clients ADD COLUMN authorized BOOLEAN NOT NULL DEFAULT 0;
-- Keep clients that completed authorization on older relays. Registrations
-- that never obtained consent get a short migration window to authorize.
UPDATE mcp_oauth_clients SET authorized = 1 WHERE EXISTS (
    SELECT 1 FROM audit_log
    WHERE event IN ('mcp_authorized', 'mcp_token_issued')
      AND detail = 'client=' || mcp_oauth_clients.client_id
);
UPDATE mcp_oauth_clients SET expires_at = MIN(expires_at, datetime('now', '+1 hour'))
    WHERE authorized = 0;
CREATE INDEX idx_mcp_oauth_clients_authorized ON mcp_oauth_clients(authorized, expires_at);
