package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/behrlich/wingthing/internal/agent"
)

type Session struct {
	ID        string        `json:"id"`
	Timestamp time.Time     `json:"timestamp"`
	Messages  []Message     `json:"messages"`
	Events    []agent.Event `json:"events"`
}

type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type Store struct {
	historyDir string
}

func NewStore(historyDir string) *Store {
	return &Store{
		historyDir: historyDir,
	}
}

func (s *Store) SaveSession(session *Session) error {
	// Ensure history directory exists
	if err := os.MkdirAll(s.historyDir, 0755); err != nil {
		return err
	}
	
	filename := session.ID + ".json"
	filePath := filepath.Join(s.historyDir, filename)
	
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	
	return os.WriteFile(filePath, data, 0644)
}

func (s *Store) LoadSession(sessionID string) (*Session, error) {
	filename := sessionID + ".json"
	filePath := filepath.Join(s.historyDir, filename)
	
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	
	return &session, nil
}

func (s *Store) LoadLastSession() (*Session, error) {
	entries, err := os.ReadDir(s.historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No history directory
		}
		return nil, err
	}
	
	var lastFile string
	var lastTime time.Time
	
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		
		info, err := entry.Info()
		if err != nil {
			continue
		}
		
		if info.ModTime().After(lastTime) {
			lastTime = info.ModTime()
			lastFile = entry.Name()
		}
	}
	
	if lastFile == "" {
		return nil, nil // No session files found
	}
	
	sessionID := lastFile[:len(lastFile)-5] // Remove .json extension
	return s.LoadSession(sessionID)
}

func (s *Store) ListSessions() ([]Session, error) {
	entries, err := os.ReadDir(s.historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Session{}, nil // No history directory
		}
		return nil, err
	}
	
	var sessions []Session
	
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		
		sessionID := entry.Name()[:len(entry.Name())-5] // Remove .json extension
		session, err := s.LoadSession(sessionID)
		if err != nil {
			continue // Skip invalid sessions
		}
		
		sessions = append(sessions, *session)
	}
	
	return sessions, nil
}

func (s *Store) DeleteSession(sessionID string) error {
	filename := sessionID + ".json"
	filePath := filepath.Join(s.historyDir, filename)
	return os.Remove(filePath)
}