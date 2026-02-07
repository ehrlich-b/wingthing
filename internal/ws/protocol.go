package ws

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type MsgType string

const (
	MsgTaskSubmit   MsgType = "task_submit"
	MsgTaskResult   MsgType = "task_result"
	MsgTaskStatus   MsgType = "task_status"
	MsgSyncRequest  MsgType = "sync_request"
	MsgSyncResponse MsgType = "sync_response"
	MsgStatus       MsgType = "status"
	MsgPing         MsgType = "ping"
	MsgPong         MsgType = "pong"
	MsgAuth         MsgType = "auth"
	MsgAuthResult   MsgType = "auth_result"
	MsgError        MsgType = "error"
)

type Message struct {
	Type      MsgType         `json:"type"`
	ID        string          `json:"id"`
	ReplyTo   string          `json:"reply_to,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp int64           `json:"timestamp"`
}

type TaskSubmitPayload struct {
	What  string `json:"what"`
	Type  string `json:"type,omitempty"`
	Agent string `json:"agent,omitempty"`
	RunAt string `json:"run_at,omitempty"`
}

type TaskResultPayload struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type TaskStatusPayload struct {
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	Progress int    `json:"progress,omitempty"`
}

type SyncRequestPayload struct {
	MachineID    string          `json:"machine_id"`
	ManifestJSON json.RawMessage `json:"manifest_json"`
}

type SyncResponsePayload struct {
	Diffs         json.RawMessage `json:"diffs"`
	ThreadEntries json.RawMessage `json:"thread_entries"`
}

type StatusPayload struct {
	Pending    int      `json:"pending"`
	Running    int      `json:"running"`
	Agents     []string `json:"agents"`
	TokensToday int     `json:"tokens_today"`
	TokensWeek  int     `json:"tokens_week"`
}

type AuthPayload struct {
	DeviceToken string `json:"device_token"`
}

type AuthResultPayload struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewMessage(msgType MsgType, payload any) (*Message, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{
		Type:      msgType,
		ID:        uuid.New().String(),
		Payload:   raw,
		Timestamp: time.Now().UnixMilli(),
	}, nil
}

func (m *Message) ParsePayload(v any) error {
	return json.Unmarshal(m.Payload, v)
}
