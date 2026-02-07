package store

import (
	"database/sql"
	"fmt"
	"time"
)

type Agent struct {
	Name             string
	Adapter          string
	Command          string
	ContextWindow    int
	DefaultIsolation string
	Healthy          bool
	HealthChecked    *time.Time
	ConfigJSON       *string
}

func (s *Store) UpsertAgent(a *Agent) error {
	_, err := s.db.Exec(`INSERT INTO agents (name, adapter, command, context_window, default_isolation, healthy, health_checked, config_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			adapter = excluded.adapter,
			command = excluded.command,
			context_window = excluded.context_window,
			default_isolation = excluded.default_isolation,
			healthy = excluded.healthy,
			health_checked = excluded.health_checked,
			config_json = excluded.config_json`,
		a.Name, a.Adapter, a.Command, a.ContextWindow, a.DefaultIsolation, a.Healthy, a.HealthChecked, a.ConfigJSON)
	if err != nil {
		return fmt.Errorf("upsert agent: %w", err)
	}
	return nil
}

func (s *Store) GetAgent(name string) (*Agent, error) {
	a := &Agent{}
	err := s.db.QueryRow(`SELECT name, adapter, command, context_window, default_isolation, healthy, health_checked, config_json
		FROM agents WHERE name = ?`, name).Scan(
		&a.Name, &a.Adapter, &a.Command, &a.ContextWindow, &a.DefaultIsolation, &a.Healthy, &a.HealthChecked, &a.ConfigJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	return a, nil
}

func (s *Store) ListAgents() ([]*Agent, error) {
	rows, err := s.db.Query(`SELECT name, adapter, command, context_window, default_isolation, healthy, health_checked, config_json
		FROM agents ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var agents []*Agent
	for rows.Next() {
		a := &Agent{}
		if err := rows.Scan(&a.Name, &a.Adapter, &a.Command, &a.ContextWindow, &a.DefaultIsolation, &a.Healthy, &a.HealthChecked, &a.ConfigJSON); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *Store) UpdateAgentHealth(name string, healthy bool, checkedAt time.Time) error {
	_, err := s.db.Exec("UPDATE agents SET healthy = ?, health_checked = ? WHERE name = ?", healthy, checkedAt.UTC(), name)
	return err
}
