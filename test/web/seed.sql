-- Canary users mirroring the Slide roost shape: one wing admin, two role members.
-- No production credentials or user data (merge-readiness promotion step 3).
INSERT OR IGNORE INTO users (id, provider, provider_id, display_name, email, tier, is_pro) VALUES
 ('u-alice','google','canary-alice','Alice Admin','alice@slide.tech','pro',1),
 ('u-bob','google','canary-bob','Bob Eng','bob@slide.tech','pro',1),
 ('u-carol','google','canary-carol','Carol Support','carol@slide.tech','pro',1);

INSERT OR IGNORE INTO sessions (token, user_id, expires_at) VALUES
 ('canary-alice-session-token-0000000001','u-alice',datetime('now','+7 day')),
 ('canary-bob-session-token-000000000002','u-bob',datetime('now','+7 day')),
 ('canary-carol-session-token-0000000003','u-carol',datetime('now','+7 day'));

INSERT OR IGNORE INTO org_members (org_id, user_id, role)
 SELECT o.id, 'u-alice', 'admin' FROM orgs o WHERE o.slug='slide';
INSERT OR IGNORE INTO org_members (org_id, user_id, role)
 SELECT o.id, 'u-bob', 'member' FROM orgs o WHERE o.slug='slide';
INSERT OR IGNORE INTO org_members (org_id, user_id, role)
 SELECT o.id, 'u-carol', 'member' FROM orgs o WHERE o.slug='slide';
