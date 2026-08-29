INSERT OR IGNORE INTO users
 (id, provider, provider_id, display_name, email, tier, is_pro, created_at) VALUES
 ('u-direct','google','canary-direct','Direct Free','direct@example.com','free',0,'2027-01-01T00:00:00Z'),
 ('u-migration','google','canary-migration','Migration User','migration@example.com','free',0,'2025-01-01T00:00:00Z'),
 ('u-pro','google','canary-pro','Pro User','pro@example.com','pro',1,'2027-01-01T00:00:00Z');

INSERT OR IGNORE INTO sessions (token, user_id, expires_at) VALUES
 ('canary-direct-session-token-000000000','u-direct',datetime('now','+7 day')),
 ('canary-migration-session-token-0000000','u-migration',datetime('now','+7 day')),
 ('canary-pro-session-token-000000000000','u-pro',datetime('now','+7 day'));

INSERT OR IGNORE INTO subscriptions (id, user_id, plan, status, seats)
 VALUES ('canary-pro-subscription','u-pro','pro_monthly','active',1);
INSERT OR IGNORE INTO entitlements (id, user_id, subscription_id)
 VALUES ('canary-pro-entitlement','u-pro','canary-pro-subscription');
