package interfaces

import "time"

// Event represents an agent event
type Event struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Data    any    `json:"data,omitempty"`
}

// Session represents a conversation session
type Session struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Messages  []Message `json:"messages"`
	Events    []Event   `json:"events"`
}

// Message represents a conversation message
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// HistoryStore handles session persistence
type HistoryStore interface {
	SaveSession(session *Session) error
	LoadSession(sessionID string) (*Session, error)
	LoadLastSession() (*Session, error)
	ListSessions() ([]Session, error)
	DeleteSession(sessionID string) error
}
