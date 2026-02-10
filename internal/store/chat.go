package store

import "time"

type ChatSession struct {
	ID        string
	Agent     string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ChatMsg struct {
	ID        int64
	SessionID string
	Role      string
	Content   string
	CreatedAt time.Time
}

func (s *Store) CreateChatSession(id, agent string) error {
	_, err := s.db.Exec(
		`INSERT INTO chat_sessions (id, agent) VALUES (?, ?)`,
		id, agent,
	)
	return err
}

func (s *Store) GetChatSession(id string) (*ChatSession, error) {
	row := s.db.QueryRow(
		`SELECT id, agent, title, created_at, updated_at FROM chat_sessions WHERE id = ?`, id,
	)
	var cs ChatSession
	if err := row.Scan(&cs.ID, &cs.Agent, &cs.Title, &cs.CreatedAt, &cs.UpdatedAt); err != nil {
		return nil, err
	}
	return &cs, nil
}

func (s *Store) ListChatSessions() ([]*ChatSession, error) {
	rows, err := s.db.Query(
		`SELECT id, agent, title, created_at, updated_at FROM chat_sessions ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*ChatSession
	for rows.Next() {
		var cs ChatSession
		if err := rows.Scan(&cs.ID, &cs.Agent, &cs.Title, &cs.CreatedAt, &cs.UpdatedAt); err != nil {
			continue
		}
		result = append(result, &cs)
	}
	return result, nil
}

func (s *Store) DeleteChatSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM chat_sessions WHERE id = ?`, id)
	return err
}

func (s *Store) AppendChatMessage(sessionID, role, content string) error {
	_, err := s.db.Exec(
		`INSERT INTO chat_messages (session_id, role, content) VALUES (?, ?, ?)`,
		sessionID, role, content,
	)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`UPDATE chat_sessions SET updated_at = datetime('now') WHERE id = ?`, sessionID,
	)
	return err
}

func (s *Store) ListChatMessages(sessionID string) ([]*ChatMsg, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, role, content, created_at FROM chat_messages WHERE session_id = ? ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*ChatMsg
	for rows.Next() {
		var m ChatMsg
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			continue
		}
		result = append(result, &m)
	}
	return result, nil
}
