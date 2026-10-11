-- Separate active and recent-terminal access paths avoid scanning archived
-- history merely to discover current work. No task/run/result data changes.
CREATE INDEX idx_tasks_run_inventory_active ON tasks(principal, strftime('%Y-%m-%dT%H:%M:%SZ',created_at), id)
WHERE type='agent_run' AND status NOT IN ('done','failed','timeout','stopped');
CREATE INDEX idx_tasks_run_inventory_recent ON tasks(principal, julianday(COALESCE(finished_at,created_at)), created_at, id)
WHERE type='agent_run' AND status IN ('done','failed','timeout','stopped');
