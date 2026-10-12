-- Original frozen orphan: submission intent in wing, completed egg receipt.
-- Hand-authored expected diagnosis: completed_with_output;
-- acknowledgement: terminal_receipt_not_in_wing; no resume recommendation.
CREATE TABLE agent_runs (id TEXT PRIMARY KEY,session_id TEXT,principal TEXT,spec_hash TEXT,revision INTEGER,record TEXT);
CREATE TABLE conversations (id TEXT PRIMARY KEY,owner_id TEXT,root_id TEXT,parent_id TEXT,session_id TEXT,checkpoint TEXT,revision INTEGER,delivered_cursor INTEGER);
CREATE TABLE conversation_executions (session_id TEXT,conversation_id TEXT);
CREATE TABLE conversation_events (sequence INTEGER,root_id TEXT,conversation_id TEXT,session_id TEXT);
CREATE TABLE conversation_wake_policy (root_id TEXT,delivery_cursor INTEGER);
INSERT INTO agent_runs VALUES ('fixture-orphan','fixture-execution','fixture-owner','fixture-wing-spec',4,'{"run_id":"fixture-orphan","spec_hash":"fixture-wing-spec","session_id":"fixture-execution","agent":"claude","cwd":"/fixture","phase":"submitting","launch":{"Authority":{"Principal":"fixture-owner","UserID":"fixture-owner"}},"result":{"run_id":"fixture-orphan","session_id":"fixture-execution","status":"pending","text":"","deadline":"2026-10-10T12:01:00Z"}}');
