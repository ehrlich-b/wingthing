package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Conversation is the durable personal task identity. SessionID identifies its
// latest execution; the execution table keeps earlier instances inspectable.
type Conversation struct {
	ID              string    `json:"conversation_id"`
	OwnerID         string    `json:"-"`
	RootID          string    `json:"root_conversation_id"`
	ParentID        string    `json:"parent_conversation_id,omitempty"`
	Title           string    `json:"title"`
	Agent           string    `json:"agent"`
	CWD             string    `json:"cwd"`
	WingID          string    `json:"wing_id"`
	SessionID       string    `json:"session_id"`
	LaunchKey       string    `json:"-"`
	SpecDigest      string    `json:"-"`
	LaunchState     string    `json:"launch_state"`
	LaunchError     string    `json:"launch_error,omitempty"`
	Checkpoint      string    `json:"checkpoint,omitempty"`
	DeliveredCursor int64     `json:"delivered_cursor"`
	Revision        int64     `json:"revision"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ConversationEvent struct {
	Sequence       int64     `json:"sequence"`
	RootID         string    `json:"root_conversation_id"`
	ConversationID string    `json:"conversation_id"`
	SessionID      string    `json:"session_id"`
	SourceCursor   int64     `json:"session_cursor"`
	State          string    `json:"state"`
	StateSource    string    `json:"state_source"`
	Type           string    `json:"type"`
	CreatedAt      time.Time `json:"created_at"`
}

const conversationColumns = `id, owner_id, root_id, parent_id, title, agent, cwd, wing_id, session_id, launch_key, spec_digest, launch_state, launch_error, checkpoint, delivered_cursor, revision, created_at, updated_at`

type conversationScanner interface{ Scan(...any) error }

func scanConversation(row conversationScanner) (*Conversation, error) {
	c := &Conversation{}
	err := row.Scan(&c.ID, &c.OwnerID, &c.RootID, &c.ParentID, &c.Title, &c.Agent, &c.CWD, &c.WingID, &c.SessionID, &c.LaunchKey, &c.SpecDigest, &c.LaunchState, &c.LaunchError, &c.Checkpoint, &c.DeliveredCursor, &c.Revision, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) GetConversation(owner, id string) (*Conversation, error) {
	return scanConversation(s.db.QueryRow(`SELECT `+conversationColumns+` FROM conversations WHERE owner_id = ? AND id = ?`, owner, id))
}

func (s *Store) ConversationForSession(session string) (*Conversation, error) {
	return scanConversation(s.db.QueryRow(`SELECT `+conversationColumns+` FROM conversations WHERE id = (SELECT conversation_id FROM conversation_executions WHERE session_id = ?)`, session))
}

// ReserveConversation atomically claims a caller-provided launch key before
// spawning. Replays return the same logical and execution IDs, even if a crash
// left the reservation in starting; callers must reconcile, never launch again.
func (s *Store) ReserveConversation(c Conversation) (*Conversation, bool, error) {
	if c.ID == "" || c.OwnerID == "" || c.SessionID == "" || c.LaunchKey == "" || c.SpecDigest == "" {
		return nil, false, errors.New("conversation reservation requires identity and launch key")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE conversations SET revision = revision WHERE id = ''`); err != nil {
		return nil, false, err
	}
	var count, existing int
	if err := tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(launch_key = ?),0) FROM conversations WHERE owner_id = ?`, c.LaunchKey, c.OwnerID).Scan(&count, &existing); err != nil {
		return nil, false, err
	}
	if count >= 256 && existing == 0 {
		return nil, false, errors.New("personal conversation inventory limit reached (256); launch an unlinked terminal or inspect existing conversations")
	}
	if c.ParentID == "" {
		c.RootID = c.ID
	} else {
		var root string
		if err := tx.QueryRow(`SELECT root_id FROM conversations WHERE owner_id = ? AND id = ?`, c.OwnerID, c.ParentID).Scan(&root); err != nil {
			return nil, false, errors.New("parent conversation not found or not owned by caller")
		}
		c.RootID = root
	}
	result, err := tx.Exec(`INSERT INTO conversations (id,owner_id,root_id,parent_id,title,agent,cwd,wing_id,session_id,launch_key,spec_digest) VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_id,launch_key) DO NOTHING`, c.ID, c.OwnerID, c.RootID, c.ParentID, c.Title, c.Agent, c.CWD, c.WingID, c.SessionID, c.LaunchKey, c.SpecDigest)
	if err != nil {
		return nil, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	saved, err := scanConversation(tx.QueryRow(`SELECT `+conversationColumns+` FROM conversations WHERE owner_id = ? AND launch_key = ?`, c.OwnerID, c.LaunchKey))
	if err != nil {
		return nil, false, err
	}
	if saved.SpecDigest != c.SpecDigest {
		return nil, false, errors.New("launch request_id was already used with different arguments")
	}
	if n == 1 {
		if _, err := tx.Exec(`INSERT INTO conversation_executions (session_id,conversation_id) VALUES (?,?)`, c.SessionID, c.ID); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return saved, n == 1, nil
}

func (s *Store) SetConversationLaunch(id, state, detail string) error {
	_, err := s.db.Exec(`UPDATE conversations SET launch_state = ?, launch_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, state, detail, id)
	return err
}

func (s *Store) ResumeConversationExecution(source, target string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	err = tx.QueryRow(`SELECT conversation_id FROM conversation_executions WHERE session_id = ?`, source).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var executions int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM conversation_executions WHERE conversation_id = ?`, id).Scan(&executions); err != nil {
		return err
	}
	if executions >= 128 {
		return errors.New("conversation execution limit reached (128); inspect existing executions before creating another conversation")
	}
	if _, err := tx.Exec(`INSERT INTO conversation_executions (session_id,conversation_id) VALUES (?,?)`, target, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE conversations SET session_id = ?, launch_state = 'started', launch_error = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, target, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConversationExecutions(id string) ([]string, error) {
	rows, err := s.db.Query(`SELECT session_id FROM conversation_executions WHERE conversation_id = ? ORDER BY created_at,session_id LIMIT 128`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0)
	for rows.Next() {
		var session string
		if err := rows.Scan(&session); err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

func (s *Store) ListConversations(owner, root string) ([]*Conversation, error) {
	query := `SELECT ` + conversationColumns + ` FROM conversations WHERE owner_id = ?`
	args := []any{owner}
	if root != "" {
		query += ` AND root_id = ?`
		args = append(args, root)
	}
	query += ` ORDER BY created_at, id LIMIT 256`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*Conversation, 0)
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RecordConversationState(c *Conversation, cursor int64, state, source string) error {
	_, err := s.db.Exec(`INSERT INTO conversation_events (root_id,conversation_id,session_id,source_cursor,state,state_source,type) VALUES (?,?,?,?,?,?,?) ON CONFLICT(session_id,source_cursor,state,state_source) DO NOTHING`, c.RootID, c.ID, c.SessionID, cursor, state, source, "state")
	return err
}

func (s *Store) ConversationImportCursor(session string) (int64, error) {
	var cursor int64
	err := s.db.QueryRow(`SELECT imported_cursor FROM conversation_executions WHERE session_id = ?`, session).Scan(&cursor)
	return cursor, err
}

// ImportConversationStates commits event deliveries and the session read cursor
// together, so reconnect replay never drops an attention or completion event.
func (s *Store) ImportConversationStates(c *Conversation, events []ConversationEvent, cursor int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, event := range events {
		if _, err := tx.Exec(`INSERT INTO conversation_events (root_id,conversation_id,session_id,source_cursor,state,state_source,type) VALUES (?,?,?,?,?,?,?) ON CONFLICT(session_id,source_cursor,state,state_source) DO NOTHING`, c.RootID, c.ID, c.SessionID, event.SourceCursor, event.State, event.StateSource, event.Type); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE conversation_executions SET imported_cursor = MAX(imported_cursor,?) WHERE session_id = ?`, cursor, c.SessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConversationEvents(owner, root string, after int64, limit int) ([]ConversationEvent, error) {
	rows, err := s.db.Query(`SELECT sequence,e.root_id,e.conversation_id,e.session_id,source_cursor,state,state_source,e.type,e.created_at FROM conversation_events e JOIN conversations c ON c.id = e.conversation_id WHERE c.owner_id = ? AND e.root_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, owner, root, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]ConversationEvent, 0)
	for rows.Next() {
		var e ConversationEvent
		if err := rows.Scan(&e.Sequence, &e.RootID, &e.ConversationID, &e.SessionID, &e.SourceCursor, &e.State, &e.StateSource, &e.Type, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// CheckpointConversation is compare-and-swap. A stale controller cannot
// overwrite newer intent or move a delivery cursor backwards or past evidence.
func (s *Store) CheckpointConversation(owner, id string, expected, after int64, checkpoint string) error {
	if len(checkpoint) > 32768 || after < 0 || expected < 0 {
		return errors.New("invalid checkpoint or cursor")
	}
	result, err := s.db.Exec(`UPDATE conversations SET checkpoint = ?, delivered_cursor = ?, revision = revision + 1, updated_at = CURRENT_TIMESTAMP WHERE owner_id = ? AND id = ? AND revision = ? AND delivered_cursor <= ? AND ? <= (SELECT COALESCE(MAX(sequence),0) FROM conversation_events WHERE root_id = conversations.root_id)`, checkpoint, after, owner, id, expected, after, after)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("checkpoint conflict: reload conversation revision and delivered cursor")
	}
	return nil
}
