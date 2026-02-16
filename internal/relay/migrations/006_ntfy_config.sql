ALTER TABLE users ADD COLUMN ntfy_topic TEXT DEFAULT '';
ALTER TABLE users ADD COLUMN ntfy_token TEXT DEFAULT '';
ALTER TABLE users ADD COLUMN ntfy_events TEXT DEFAULT 'attention,exit';
