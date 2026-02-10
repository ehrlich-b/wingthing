package ws

import "time"

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

	// Relay → Wing (control)
	TypeRegistered = "registered"
	TypeError      = "error"
)

// Envelope wraps every WebSocket message with a type field for routing.
type Envelope struct {
	Type string `json:"type"`
}

// WingRegister is sent by the wing on connect.
type WingRegister struct {
	Type       string   `json:"type"`
	MachineID  string   `json:"machine_id"`
	Agents     []string `json:"agents"`
	Skills     []string `json:"skills"`
	Labels     []string `json:"labels"`
	Identities []string `json:"identities"`
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
}

// PTYStarted confirms the PTY session is running.
type PTYStarted struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	PublicKey string `json:"public_key,omitempty"` // wing's X25519 (base64)
}

// PTYOutput carries raw terminal bytes from wing to browser.
type PTYOutput struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Data      string `json:"data"` // base64-encoded
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
