package store

import (
	"database/sql"
	"errors"
)

type ConversationContinuation struct {
	OwnerID, RequestID, ConversationID, SourceSession, SessionID string
	ProviderSessionID, InputSHA256, SpecDigest                   string
	LaunchState, LaunchError                                     string
	PriorLaunchState, PriorLaunchError                           string
}

func scanContinuation(row conversationScanner) (*ConversationContinuation, error) {
	c := &ConversationContinuation{}
	err := row.Scan(&c.OwnerID, &c.RequestID, &c.ConversationID, &c.SourceSession, &c.SessionID, &c.ProviderSessionID, &c.InputSHA256, &c.SpecDigest, &c.LaunchState, &c.LaunchError, &c.PriorLaunchState, &c.PriorLaunchError)
	return c, err
}

const continuationColumns = `owner_id,request_id,conversation_id,source_session,session_id,provider_session_id,input_sha256,spec_digest,launch_state,launch_error,prior_launch_state,prior_launch_error`

func (s *Store) GetConversationContinuation(owner, request string) (*ConversationContinuation, error) {
	return scanContinuation(s.db.QueryRow(`SELECT `+continuationColumns+` FROM conversation_continuations WHERE owner_id = ? AND request_id = ?`, owner, request))
}

// ReserveConversationContinuation claims the retry key and links the execution
// before dispatch. A crash leaves starting evidence; replay never dispatches it.
func (s *Store) ReserveConversationContinuation(c ConversationContinuation) (*ConversationContinuation, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE conversations SET revision = revision WHERE id = ''`); err != nil {
		return nil, false, err
	}
	existing, err := scanContinuation(tx.QueryRow(`SELECT `+continuationColumns+` FROM conversation_continuations WHERE owner_id = ? AND request_id = ?`, c.OwnerID, c.RequestID))
	if err == nil {
		if existing.SpecDigest != c.SpecDigest {
			return nil, false, errors.New("launch request_id was already used with different arguments")
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	var current, root, parent, agent string
	if err := tx.QueryRow(`SELECT session_id,root_id,parent_id,agent,launch_state,launch_error FROM conversations WHERE owner_id = ? AND id = ?`, c.OwnerID, c.ConversationID).Scan(&current, &root, &parent, &agent, &c.PriorLaunchState, &c.PriorLaunchError); err != nil {
		return nil, false, err
	}
	if current != c.SourceSession || root != c.ConversationID || parent != "" || agent != "claude" {
		return nil, false, errors.New("continuation requires the current Claude root execution")
	}
	var conflicts, executions int
	if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM conversations WHERE owner_id = ? AND launch_key = ?) + (SELECT COUNT(*) FROM conversation_continuations WHERE conversation_id = ? AND launch_state = 'starting'), (SELECT COUNT(*) FROM conversation_executions WHERE conversation_id = ?)`, c.OwnerID, c.RequestID, c.ConversationID, c.ConversationID).Scan(&conflicts, &executions); err != nil {
		return nil, false, err
	}
	if conflicts != 0 {
		return nil, false, errors.New("request_id is already used or this conversation has an unconfirmed continuation")
	}
	if executions >= 128 {
		return nil, false, errors.New("conversation execution limit reached (128)")
	}
	if _, err := tx.Exec(`INSERT INTO conversation_executions (session_id,conversation_id) VALUES (?,?)`, c.SessionID, c.ConversationID); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`INSERT INTO conversation_continuations (owner_id,request_id,conversation_id,source_session,session_id,provider_session_id,input_sha256,spec_digest,prior_launch_state,prior_launch_error) VALUES (?,?,?,?,?,?,?,?,?,?)`, c.OwnerID, c.RequestID, c.ConversationID, c.SourceSession, c.SessionID, c.ProviderSessionID, c.InputSHA256, c.SpecDigest, c.PriorLaunchState, c.PriorLaunchError); err != nil {
		return nil, false, err
	}
	// Bound MCP can run as soon as the provider starts. Its root must already
	// resolve to this invocation, even before launch acknowledgement returns.
	if _, err := tx.Exec(`UPDATE conversations SET session_id = ?, launch_state = 'starting', launch_error = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, c.SessionID, c.ConversationID); err != nil {
		return nil, false, err
	}
	c.LaunchState = "starting"
	return &c, true, tx.Commit()
}

func (s *Store) SetConversationContinuationLaunch(c *ConversationContinuation, state, detail string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE conversation_continuations SET launch_state = ?, launch_error = ? WHERE owner_id = ? AND request_id = ? AND launch_state = 'starting'`, state, detail, c.OwnerID, c.RequestID); err != nil {
		return err
	}
	session, rootState, rootError := c.SessionID, state, detail
	if state == "failed" {
		session, rootState, rootError = c.SourceSession, c.PriorLaunchState, c.PriorLaunchError
	}
	// The existing terminal-resume path can advance the root independently.
	// Finalize this request's evidence without overwriting a newer execution.
	if _, err := tx.Exec(`UPDATE conversations SET session_id = ?, launch_state = ?, launch_error = ?, updated_at = CURRENT_TIMESTAMP WHERE owner_id = ? AND id = ? AND session_id = ?`, session, rootState, rootError, c.OwnerID, c.ConversationID, c.SessionID); err != nil {
		return err
	}
	return tx.Commit()
}
