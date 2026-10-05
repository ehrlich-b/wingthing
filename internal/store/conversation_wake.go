package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

type ConversationWakePolicy struct {
	Enabled        bool  `json:"enabled"`
	ScanCursor     int64 `json:"scan_cursor"`
	DeliveryCursor int64 `json:"wake_delivery_cursor"`
}

const MaxConversationWakeAttempts = 12

type ConversationWake struct {
	RootID            string            `json:"root_conversation_id"`
	EventSequence     int64             `json:"event_cursor"`
	Attempt           int               `json:"attempt"`
	AttemptLimit      int               `json:"attempt_limit"`
	RequestID         string            `json:"request_id,omitempty"`
	SessionID         string            `json:"session_id,omitempty"`
	ProviderSessionID string            `json:"provider_session_id,omitempty"`
	Input             string            `json:"-"`
	Status            string            `json:"status"`
	ReceiptCursor     int64             `json:"receipt_cursor,omitempty"`
	RetryAfter        int64             `json:"retry_after,omitempty"`
	Reason            string            `json:"reason,omitempty"`
	Event             ConversationEvent `json:"event"`
}

func (s *Store) SetConversationWake(owner, root string, enabled bool) error {
	c, err := s.GetConversation(owner, root)
	if err != nil || c.ParentID != "" {
		return errors.New("wake requires an owned root conversation")
	}
	_, err = s.db.Exec(`INSERT INTO conversation_wake_policy(root_id,enabled) VALUES (?,?) ON CONFLICT(root_id) DO UPDATE SET enabled=excluded.enabled`, root, enabled)
	return err
}

func (s *Store) ConversationWakePolicy(root string) (ConversationWakePolicy, error) {
	var p ConversationWakePolicy
	err := s.db.QueryRow(`SELECT enabled,scan_cursor,delivery_cursor FROM conversation_wake_policy WHERE root_id=?`, root).Scan(&p.Enabled, &p.ScanCursor, &p.DeliveryCursor)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	return p, err
}

func (s *Store) ConversationWakeRoots(after string, limit int) ([]*Conversation, error) {
	if limit < 1 || limit > 15 {
		return nil, errors.New("wake root page must be between 1 and 15")
	}
	rows, err := s.db.Query(`SELECT `+qualifiedConversationColumns+` FROM conversations c JOIN conversation_wake_policy p ON p.root_id=c.id WHERE p.enabled=1 AND c.id>? ORDER BY c.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roots []*Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		roots = append(roots, c)
	}
	return roots, rows.Err()
}

const qualifiedConversationColumns = `c.id,c.owner_id,c.root_id,c.parent_id,c.title,c.agent,c.cwd,c.wing_id,c.session_id,c.launch_key,c.spec_digest,c.launch_state,c.launch_error,c.checkpoint,c.delivered_cursor,c.revision,c.created_at,c.updated_at`

func (s *Store) PendingConversationWake(root string) (*ConversationWake, error) {
	w := new(ConversationWake)
	err := s.db.QueryRow(`SELECT w.root_id,w.event_sequence,w.attempt,w.attempt_limit,w.request_id,w.session_id,w.provider_session_id,w.input,w.status,w.receipt_cursor,w.retry_after,w.reason,e.root_id,e.conversation_id,e.session_id,e.source_cursor,e.state,e.state_source,e.type,e.created_at FROM conversation_wake_outbox w JOIN conversation_events e ON e.sequence=w.event_sequence WHERE w.root_id=? AND w.status<>'observed' ORDER BY w.event_sequence LIMIT 1`, root).Scan(&w.RootID, &w.EventSequence, &w.Attempt, &w.AttemptLimit, &w.RequestID, &w.SessionID, &w.ProviderSessionID, &w.Input, &w.Status, &w.ReceiptCursor, &w.RetryAfter, &w.Reason, &w.Event.RootID, &w.Event.ConversationID, &w.Event.SessionID, &w.Event.SourceCursor, &w.Event.State, &w.Event.StateSource, &w.Event.Type, &w.Event.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	w.Event.Sequence = w.EventSequence
	return w, err
}

// At most one event is outstanding per root. Scanning and reservation commit
// together; reopening the store cannot create a second wake for that event.
func (s *Store) QueueConversationWake(root string) (*ConversationWake, error) {
	if w, err := s.PendingConversationWake(root); err != nil || w != nil {
		return w, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var enabled bool
	var after int64
	if err = tx.QueryRow(`SELECT enabled,scan_cursor FROM conversation_wake_policy WHERE root_id=?`, root).Scan(&enabled, &after); err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	rows, err := tx.Query(`SELECT sequence,conversation_id,state,state_source,type FROM conversation_events WHERE root_id=? AND sequence>? ORDER BY sequence LIMIT 50`, root, after)
	if err != nil {
		return nil, err
	}
	var selected int64
	for rows.Next() {
		var id, state, source, eventType string
		if err = rows.Scan(&after, &id, &state, &source, &eventType); err != nil {
			rows.Close()
			return nil, err
		}
		native := (source == "claude_hook" || source == "claude_transcript") && (state == "needs_input" || state == "completed" || state == "stopped" || state == "failed")
		process := source == "egg_process" && (eventType == "session_exit" || eventType == "session_failed") && (state == "stopped" || state == "failed")
		if id != root && (native || process) {
			selected = after
			break
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if selected != 0 {
		if _, err = tx.Exec(`INSERT INTO conversation_wake_outbox(root_id,event_sequence) VALUES (?,?) ON CONFLICT DO NOTHING`, root, selected); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(`UPDATE conversation_wake_policy SET scan_cursor=MAX(scan_cursor,?) WHERE root_id=?`, after, root); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.PendingConversationWake(root)
}

// Only queued or explicitly proven-not-sent attempts may acquire a new ID or
// destination. Unknown delivery keeps its immutable request and provider binding.
func (s *Store) BindConversationWake(w *ConversationWake, session, provider, input string, now int64) error {
	if session == "" || provider == "" || input == "" || len(input) > 4096 {
		return errors.New("wake binding requires bounded exact target and input")
	}
	if w.Status != "queued" && w.Status != "not_sent" {
		return errors.New("wake may already have delivered input")
	}
	if w.Attempt >= w.AttemptLimit || w.AttemptLimit > MaxConversationWakeAttempts || w.RetryAfter > now {
		return errors.New("wake retry limit or cooldown")
	}
	attempt := w.Attempt + 1
	request := fmt.Sprintf("wake-%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", w.RootID, w.EventSequence, attempt))))
	result, err := s.db.Exec(`UPDATE conversation_wake_outbox SET attempt=?,request_id=?,session_id=?,provider_session_id=?,input=?,status='pending',reason='',retry_after=0 WHERE root_id=? AND event_sequence=? AND attempt=? AND attempt_limit=? AND status IN ('queued','not_sent')`, attempt, request, session, provider, input, w.RootID, w.EventSequence, w.Attempt, w.AttemptLimit)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("wake binding conflict")
	}
	w.Attempt, w.RequestID, w.SessionID, w.ProviderSessionID, w.Input, w.Status = attempt, request, session, provider, input, "pending"
	return nil
}

func (s *Store) RecordConversationWake(w *ConversationWake, status, reason string, receipt, now int64) error {
	if status != "observed" && status != "not_sent" && status != "unconfirmed" {
		return errors.New("invalid wake outcome")
	}
	if len(reason) > 256 {
		reason = reason[:256]
	}
	retry := int64(0)
	if status == "not_sent" {
		retry = now + 5
		if w.Attempt >= w.AttemptLimit {
			status = "blocked"
			reason = "known-not-sent retry limit reached"
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE conversation_wake_outbox SET status=?,reason=?,receipt_cursor=?,retry_after=? WHERE root_id=? AND event_sequence=? AND request_id=? AND status IN ('pending','unconfirmed')`, status, reason, receipt, retry, w.RootID, w.EventSequence, w.RequestID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("wake outcome conflict")
	}
	if status == "observed" {
		if _, err = tx.Exec(`UPDATE conversation_wake_policy SET delivery_cursor=MAX(delivery_cursor,?) WHERE root_id=?`, w.EventSequence, w.RootID); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM conversation_wake_outbox WHERE root_id=? AND status='observed' AND event_sequence NOT IN (SELECT event_sequence FROM conversation_wake_outbox WHERE root_id=? AND status='observed' ORDER BY event_sequence DESC LIMIT 128)`, w.RootID, w.RootID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Explicit recovery is permitted only by a retained known-no-input outcome.
// Attempt numbers and old request evidence never reset or become ambiguous.
func (s *Store) RetryNotSentConversationWake(owner, root string, now int64) error {
	c, err := s.GetConversation(owner, root)
	if err != nil || c.ParentID != "" {
		return errors.New("wake retry requires an owned root conversation")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sequence int64
	var attempt, limit int
	var status string
	err = tx.QueryRow(`SELECT event_sequence,attempt,attempt_limit,status FROM conversation_wake_outbox WHERE root_id=? AND status<>'observed' ORDER BY event_sequence LIMIT 1`, root).Scan(&sequence, &attempt, &limit, &status)
	if err != nil {
		return errors.New("no proven-not-sent wake is pending")
	}
	if attempt < 1 || (status != "not_sent" && status != "blocked") {
		return errors.New("wake retry requires explicit definitely-not-sent evidence; ambiguous delivery cannot be retried")
	}
	if limit >= MaxConversationWakeAttempts {
		return errors.New("wake total attempt cap reached (12)")
	}
	result, err := tx.Exec(`UPDATE conversation_wake_outbox SET attempt_limit=attempt_limit+3,status='not_sent',retry_after=MAX(retry_after,?),reason='explicit retry authorized after proven no-input failure' WHERE root_id=? AND event_sequence=? AND status=? AND attempt=? AND attempt_limit=?`, now+5, root, sequence, status, attempt, limit)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("wake retry conflict; inspect current delivery evidence")
	}
	return tx.Commit()
}
