package ws

import (
	"context"
	"time"
)

// Message types for the relay WebSocket protocol.
const (
	// Wing → Relay
	TypeWingRegister  = "wing.register"
	TypeWingHeartbeat = "wing.heartbeat"
	TypeTaskChunk     = "task.chunk"
	TypeTaskDone      = "task.done"
	TypeTaskError     = "task.error"

	// Relay → Wing
	TypeTaskSubmit = "task.submit"
	TypeTaskCancel = "task.cancel"

	// PTY (bidirectional)
	TypePTYStart   = "pty.start"   // browser → relay → wing
	TypePTYStarted = "pty.started" // wing → relay → browser
	TypePTYOutput  = "pty.output"  // wing → relay → browser
	TypePTYInput   = "pty.input"   // browser → relay → wing
	TypePTYResize  = "pty.resize"  // browser → relay → wing
	TypePTYExited  = "pty.exited"  // wing → relay → browser
	TypePTYAttach  = "pty.attach"  // browser → relay → wing (reattach)
	TypePTYKill    = "pty.kill"    // browser → relay → wing (terminate session)
	TypePTYDetach       = "pty.detach"        // browser → relay (explicit detach before disconnect)
	TypePTYAttentionAck = "pty.attention_ack" // browser → relay → wing (notification seen)

	// Chat (bidirectional through relay)
	TypeChatStart   = "chat.start"   // browser → relay → wing
	TypeChatStarted = "chat.started" // wing → relay → browser
	TypeChatMessage = "chat.message" // browser → relay → wing
	TypeChatChunk   = "chat.chunk"   // wing → relay → browser
	TypeChatDone    = "chat.done"    // wing → relay → browser
	TypeChatHistory = "chat.history" // wing → relay → browser
	TypeChatDelete  = "chat.delete"  // browser → relay → wing
	TypeChatDeleted = "chat.deleted" // wing → relay → browser

	// Directory listing (bidirectional through relay)
	TypeDirList    = "dir.list"    // browser → relay → wing
	TypeDirResults = "dir.results" // wing → relay → browser

	// Wing → Relay (session reclaim after wing restart)
	TypePTYReclaim = "pty.reclaim"

	// Session sync (relay requests, wing responds; also sent on heartbeat)
	TypeSessionsList = "sessions.list" // relay → wing
	TypeSessionsSync = "sessions.sync" // wing → relay

	// Relay → Wing (control)
	TypeRegistered     = "registered"
	TypeWingUpdate     = "wing.update"
	TypeEggConfigUpdate = "egg.config_update" // relay → wing: push new egg config
	TypeError          = "error"
)

// Envelope wraps every WebSocket message with a type field for routing.
type Envelope struct {
	Type string `json:"type"`
}

// WingProject is a project directory discovered on the wing.
type WingProject struct {
	Name    string `json:"name"`               // directory name (e.g. "wingthing")
	Path    string `json:"path"`               // absolute path (e.g. "/Users/ehrlich/repos/wingthing")
	ModTime int64  `json:"mod_time,omitempty"` // unix timestamp of last modification
}

// WingRegister is sent by the wing on connect.
type WingRegister struct {
	Type       string        `json:"type"`
	MachineID  string        `json:"machine_id"`
	Platform   string        `json:"platform,omitempty"` // runtime.GOOS (e.g. "darwin", "linux")
	Version    string        `json:"version,omitempty"`  // build version (e.g. "v0.7.35")
	Agents     []string      `json:"agents"`
	Skills     []string      `json:"skills"`
	Labels     []string      `json:"labels"`
	Identities []string      `json:"identities"`
	Projects   []WingProject `json:"projects,omitempty"`
}

// WingHeartbeat is sent by the wing every 30s.
type WingHeartbeat struct {
	Type      string `json:"type"`
	MachineID string `json:"machine_id"`
}

// TaskSubmit is sent from the relay to a wing to execute a task.
type TaskSubmit struct {
	Type      string `json:"type"`
	TaskID    string `json:"task_id"`
	Prompt    string `json:"prompt"`
	Skill     string `json:"skill,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Isolation string `json:"isolation,omitempty"`
}

// TaskChunk is streamed from the wing back to the relay during execution.
type TaskChunk struct {
	Type   string `json:"type"`
	TaskID string `json:"task_id"`
	Text   string `json:"text"`
}

// TaskDone is sent by the wing when a task completes successfully.
type TaskDone struct {
	Type   string `json:"type"`
	TaskID string `json:"task_id"`
}

// TaskErrorMsg is sent by the wing when a task fails.
type TaskErrorMsg struct {
	Type   string `json:"type"`
	TaskID string `json:"task_id"`
	Error  string `json:"error"`
}

// RegisteredMsg is the relay's acknowledgment of a successful wing registration.
type RegisteredMsg struct {
	Type   string `json:"type"`
	WingID string `json:"wing_id"`
}

// ErrorMsg is sent by the relay for protocol errors.
type ErrorMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// PTYStart requests a new interactive terminal session on the wing.
type PTYStart struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"` // "claude", "codex", "ollama"
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	PublicKey string `json:"public_key,omitempty"` // browser's ephemeral X25519 (base64)
	CWD       string `json:"cwd,omitempty"`        // working directory for the agent
	WingID    string `json:"wing_id,omitempty"`   // target wing (picks first if empty)
}

// PTYStarted confirms the PTY session is running.
type PTYStarted struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	PublicKey string `json:"public_key,omitempty"` // wing's X25519 (base64)
	CWD       string `json:"cwd,omitempty"`        // resolved working directory
}

// PTYOutput carries raw terminal bytes from wing to browser.
type PTYOutput struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id"`
	Data       string `json:"data"`                  // base64-encoded
	Compressed bool   `json:"compressed,omitempty"` // gzip before encrypt
}

// PTYInput carries keystrokes from browser to wing.
type PTYInput struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Data      string `json:"data"` // base64-encoded
}

// PTYResize tells the wing to resize the terminal.
type PTYResize struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// PTYExited tells the browser the process exited.
type PTYExited struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	ExitCode  int    `json:"exit_code"`
	Error     string `json:"error,omitempty"` // crash/error info for display
}

// PTYAttach requests reattachment to an existing PTY session.
type PTYAttach struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	PublicKey string `json:"public_key,omitempty"` // new browser ephemeral key
}

// PTYKill requests termination of a PTY session.
type PTYKill struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// PTYDetach explicitly detaches the browser from a PTY session.
type PTYDetach struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// PTYAttentionAck acknowledges a notification was seen by the browser.
type PTYAttentionAck struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// ChatStart requests a new or resumed chat session.
type ChatStart struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"` // empty = new, set = resume
	Agent     string `json:"agent"`
}

// ChatStarted confirms the chat session is ready.
type ChatStarted struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
}

// ChatMessage carries a user message to the wing.
type ChatMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
}

// ChatChunk carries a streaming response chunk from wing to browser.
type ChatChunk struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// ChatDone signals the assistant response is complete.
type ChatDone struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"` // full assistant response
}

// ChatHistoryMsg carries conversation history for a resumed session.
type ChatHistoryMsg struct {
	Type      string             `json:"type"`
	SessionID string             `json:"session_id"`
	Messages  []ChatHistoryEntry `json:"messages"`
}

// ChatHistoryEntry is a single message in the history payload.
type ChatHistoryEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatDelete requests deletion of a chat session.
type ChatDelete struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// ChatDeleted confirms a chat session was deleted.
type ChatDeleted struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// DirList requests a directory listing from the wing.
type DirList struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Path      string `json:"path"`
	WingID    string `json:"wing_id,omitempty"`
}

// DirResults returns directory entries from the wing.
type DirResults struct {
	Type      string     `json:"type"`
	RequestID string     `json:"request_id"`
	Entries   []DirEntry `json:"entries"`
}

// DirEntry is a single entry in a directory listing.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Path  string `json:"path"`
}

// DirHandler is called when the wing receives a dir.list request.
type DirHandler func(ctx context.Context, req DirList, write PTYWriteFunc)

// PTYReclaim is sent by the wing after reconnect to reclaim a surviving egg session.
type PTYReclaim struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	CWD       string `json:"cwd,omitempty"`
}

// WingUpdate tells the wing to self-update to the latest release.
type WingUpdate struct {
	Type string `json:"type"`
}

// SessionsList requests the wing's current session list.
type SessionsList struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

// SessionsSync carries the wing's current session list.
type SessionsSync struct {
	Type      string        `json:"type"`
	RequestID string        `json:"request_id,omitempty"` // set when responding to sessions.list
	Sessions  []SessionInfo `json:"sessions"`
}

// SessionInfo describes one active session on a wing.
type SessionInfo struct {
	SessionID      string `json:"session_id"`
	Agent          string `json:"agent"`
	CWD            string `json:"cwd,omitempty"`
	EggConfig      string `json:"egg_config,omitempty"` // YAML config snapshot
	NeedsAttention bool   `json:"needs_attention,omitempty"`
}

// EggConfigUpdate pushes a new egg config from relay to wing.
type EggConfigUpdate struct {
	Type string `json:"type"`
	YAML string `json:"yaml"` // serialized egg.yaml content
}

// QueuedTask is a routing entry in the relay's volatile queue.
// The relay stores only an opaque payload — it never inspects task content.
type QueuedTask struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Identity  string    `json:"identity"`
	Payload   string    `json:"-"`         // opaque JSON forwarded to wing
	WingID    string    `json:"wing_id,omitempty"`
	Status    string    `json:"status"`    // pending, dispatched
	CreatedAt time.Time `json:"created_at"`
}
