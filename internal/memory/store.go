package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store handles database operations
type Store struct {
	db *sql.DB
}

// Message represents a Discord message
type Message struct {
	ID        int64
	DiscordID string
	UserID    string
	Content   string
	Timestamp time.Time
	CreatedAt time.Time
}

// Memory represents a stored fact/pattern/boundary
type Memory struct {
	ID        int64
	Type      string // "fact", "relationship", "boundary", "pattern"
	Content   string
	Metadata  map[string]interface{}
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Dream represents a Morning Card
type Dream struct {
	ID               int64
	Date             string
	YesterdaySummary []string
	OpenLoops        []string
	SupportPlan      []SupportAction
	DeliveredAt      *time.Time
	Acknowledged     bool
	CreatedAt        time.Time
}

// SupportAction represents an action in the Morning Card
type SupportAction struct {
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	Draft  string `json:"draft"`
}

// NewStore creates a new database store and initializes schema
func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	store := &Store{db: db}
	if err := store.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// initSchema creates tables if they don't exist
func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		discord_id TEXT UNIQUE NOT NULL,
		user_id TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp DATETIME NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS memories (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		content TEXT NOT NULL,
		metadata JSON,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS dreams (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date DATE UNIQUE NOT NULL,
		yesterday_summary JSON NOT NULL,
		open_loops JSON NOT NULL,
		support_plan JSON NOT NULL,
		delivered_at DATETIME,
		acknowledged BOOLEAN DEFAULT FALSE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp);
	CREATE INDEX IF NOT EXISTS idx_memories_type ON memories(type);
	CREATE INDEX IF NOT EXISTS idx_dreams_date ON dreams(date);
	`

	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	return nil
}

// SaveMessage stores a Discord message
func (s *Store) SaveMessage(msg *Message) error {
	query := `
		INSERT INTO messages (discord_id, user_id, content, timestamp)
		VALUES (?, ?, ?, ?)
	`
	_, err := s.db.Exec(query, msg.DiscordID, msg.UserID, msg.Content, msg.Timestamp)
	if err != nil {
		return fmt.Errorf("failed to save message: %w", err)
	}
	return nil
}

// GetRecentMessages retrieves messages from the last N hours
func (s *Store) GetRecentMessages(hours int) ([]Message, error) {
	query := `
		SELECT id, discord_id, user_id, content, timestamp, created_at
		FROM messages
		WHERE timestamp > datetime('now', '-' || ? || ' hours')
		ORDER BY timestamp ASC
	`

	rows, err := s.db.Query(query, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.DiscordID, &msg.UserID, &msg.Content, &msg.Timestamp, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		messages = append(messages, msg)
	}

	return messages, nil
}

// SaveMemory stores a memory
func (s *Store) SaveMemory(mem *Memory) error {
	metadata, err := json.Marshal(mem.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	query := `
		INSERT INTO memories (type, content, metadata)
		VALUES (?, ?, ?)
	`
	_, err = s.db.Exec(query, mem.Type, mem.Content, metadata)
	if err != nil {
		return fmt.Errorf("failed to save memory: %w", err)
	}
	return nil
}

// GetMemories retrieves all memories, optionally filtered by type
func (s *Store) GetMemories(memType string) ([]Memory, error) {
	var query string
	var args []interface{}

	if memType != "" {
		query = `
			SELECT id, type, content, metadata, created_at, updated_at
			FROM memories
			WHERE type = ?
			ORDER BY created_at DESC
		`
		args = append(args, memType)
	} else {
		query = `
			SELECT id, type, content, metadata, created_at, updated_at
			FROM memories
			ORDER BY created_at DESC
		`
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var mem Memory
		var metadataJSON []byte
		if err := rows.Scan(&mem.ID, &mem.Type, &mem.Content, &metadataJSON, &mem.CreatedAt, &mem.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan memory: %w", err)
		}
		if err := json.Unmarshal(metadataJSON, &mem.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
		}
		memories = append(memories, mem)
	}

	return memories, nil
}

// SaveDream stores a Morning Card
func (s *Store) SaveDream(dream *Dream) error {
	yesterday, err := json.Marshal(dream.YesterdaySummary)
	if err != nil {
		return fmt.Errorf("failed to marshal yesterday_summary: %w", err)
	}

	openLoops, err := json.Marshal(dream.OpenLoops)
	if err != nil {
		return fmt.Errorf("failed to marshal open_loops: %w", err)
	}

	supportPlan, err := json.Marshal(dream.SupportPlan)
	if err != nil {
		return fmt.Errorf("failed to marshal support_plan: %w", err)
	}

	query := `
		INSERT INTO dreams (date, yesterday_summary, open_loops, support_plan)
		VALUES (?, ?, ?, ?)
	`
	_, err = s.db.Exec(query, dream.Date, yesterday, openLoops, supportPlan)
	if err != nil {
		return fmt.Errorf("failed to save dream: %w", err)
	}
	return nil
}

// GetLatestDream retrieves the most recent Dream
func (s *Store) GetLatestDream() (*Dream, error) {
	query := `
		SELECT id, date, yesterday_summary, open_loops, support_plan, delivered_at, acknowledged, created_at
		FROM dreams
		ORDER BY date DESC
		LIMIT 1
	`

	var dream Dream
	var yesterdayJSON, loopsJSON, planJSON []byte
	var deliveredAt sql.NullTime

	err := s.db.QueryRow(query).Scan(
		&dream.ID, &dream.Date, &yesterdayJSON, &loopsJSON, &planJSON,
		&deliveredAt, &dream.Acknowledged, &dream.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query dream: %w", err)
	}

	if deliveredAt.Valid {
		dream.DeliveredAt = &deliveredAt.Time
	}

	if err := json.Unmarshal(yesterdayJSON, &dream.YesterdaySummary); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yesterday_summary: %w", err)
	}
	if err := json.Unmarshal(loopsJSON, &dream.OpenLoops); err != nil {
		return nil, fmt.Errorf("failed to unmarshal open_loops: %w", err)
	}
	if err := json.Unmarshal(planJSON, &dream.SupportPlan); err != nil {
		return nil, fmt.Errorf("failed to unmarshal support_plan: %w", err)
	}

	return &dream, nil
}

// MarkDreamDelivered marks a dream as delivered
func (s *Store) MarkDreamDelivered(dreamID int64) error {
	query := `UPDATE dreams SET delivered_at = CURRENT_TIMESTAMP WHERE id = ?`
	_, err := s.db.Exec(query, dreamID)
	return err
}
