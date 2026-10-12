-- Deliberately partial copies of migrations 014 and 018, not a new schema
-- version: keep identity columns but omit the unavailable acknowledgement/CAS
-- fields. Executed only while constructing a disposable fixture, before seal.
ALTER TABLE agent_runs DROP COLUMN revision;
ALTER TABLE conversations DROP COLUMN revision;
ALTER TABLE conversations DROP COLUMN delivered_cursor;
