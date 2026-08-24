package store

import (
	"database/sql"
	"fmt"
	"time"
)

type Message struct {
	ID             int64
	MessageID      string
	OwnerID        string
	SenderActor    string
	RecipientActor string
	Channel        string
	Kind           string
	ReplyTo        string
	Content        string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

func (s *Store) CreateMessage(message *Message) error {
	if message == nil {
		return fmt.Errorf("create message: message is required")
	}
	if message.Kind == "" {
		message.Kind = "message"
	}
	if message.ExpiresAt.IsZero() {
		message.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
	}
	if message.ReplyTo != "" {
		var owner string
		err := s.db.QueryRow(`SELECT owner_id FROM messages WHERE message_id = ?`, message.ReplyTo).Scan(&owner)
		if err == sql.ErrNoRows {
			return fmt.Errorf("create message: reply target not found or not owned by caller")
		}
		if err != nil {
			return fmt.Errorf("create message: resolve reply target: %w", err)
		}
		if owner != message.OwnerID {
			return fmt.Errorf("create message: reply target not found or not owned by caller")
		}
	}
	result, err := s.db.Exec(`INSERT INTO messages
		(message_id, owner_id, sender_actor, recipient_actor, channel, kind, reply_to, content, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
		message.MessageID, message.OwnerID, message.SenderActor, message.RecipientActor,
		message.Channel, message.Kind, message.ReplyTo, message.Content,
		message.ExpiresAt.UTC().Format(timeFmt))
	if err != nil {
		return fmt.Errorf("create message: %w", err)
	}
	message.ID, _ = result.LastInsertId()
	created, err := s.GetMessage(message.OwnerID, message.MessageID)
	if err != nil {
		return err
	}
	if created != nil {
		message.CreatedAt = created.CreatedAt
	}
	return nil
}

func (s *Store) PurgeExpiredMessages() error {
	if _, err := s.db.Exec(`DELETE FROM messages WHERE expires_at <= ?`, time.Now().UTC().Format(timeFmt)); err != nil {
		return fmt.Errorf("expire messages: %w", err)
	}
	return nil
}

func (s *Store) GetMessage(ownerID, messageID string) (*Message, error) {
	message := &Message{}
	var createdAt, expiresAt string
	var replyTo sql.NullString
	err := s.db.QueryRow(`SELECT id, message_id, owner_id, sender_actor, recipient_actor,
		channel, kind, reply_to, content, created_at, expires_at
		FROM messages WHERE owner_id = ? AND message_id = ?`, ownerID, messageID).Scan(
		&message.ID, &message.MessageID, &message.OwnerID, &message.SenderActor,
		&message.RecipientActor, &message.Channel, &message.Kind, &replyTo,
		&message.Content, &createdAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	if replyTo.Valid {
		message.ReplyTo = replyTo.String
	}
	message.CreatedAt = parseTime(createdAt)
	message.ExpiresAt = parseTime(expiresAt)
	return message, nil
}

// ListMessages returns messages visible to actor in ascending durable order.
// Broadcasts and messages addressed to actor are visible. Sent messages are
// omitted unless includeSent is requested.
func (s *Store) ListMessages(ownerID, actor, channel, afterMessageID string, limit int, includeSent bool) ([]*Message, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var afterID int64
	if afterMessageID != "" {
		err := s.db.QueryRow(`SELECT id FROM messages WHERE owner_id = ? AND message_id = ?`, ownerID, afterMessageID).Scan(&afterID)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("message cursor not found or not owned by caller")
		}
		if err != nil {
			return nil, fmt.Errorf("resolve message cursor: %w", err)
		}
	}

	query := `SELECT id, message_id, owner_id, sender_actor, recipient_actor,
		channel, kind, reply_to, content, created_at, expires_at
		FROM messages
		WHERE owner_id = ? AND channel = ? AND id > ? AND expires_at > ?`
	args := []any{ownerID, channel, afterID, time.Now().UTC().Format(timeFmt)}
	if includeSent {
		query += ` AND (recipient_actor = '' OR recipient_actor = ? OR sender_actor = ?)`
		args = append(args, actor, actor)
	} else {
		query += ` AND (recipient_actor = '' OR recipient_actor = ?) AND sender_actor <> ?`
		args = append(args, actor, actor)
	}
	query += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		message := &Message{}
		var createdAt, expiresAt string
		var replyTo sql.NullString
		if err := rows.Scan(&message.ID, &message.MessageID, &message.OwnerID,
			&message.SenderActor, &message.RecipientActor, &message.Channel,
			&message.Kind, &replyTo, &message.Content, &createdAt, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if replyTo.Valid {
			message.ReplyTo = replyTo.String
		}
		message.CreatedAt = parseTime(createdAt)
		message.ExpiresAt = parseTime(expiresAt)
		messages = append(messages, message)
	}
	return messages, rows.Err()
}
