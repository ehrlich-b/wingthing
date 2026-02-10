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
	Output string `json:"output"`
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

// RelayTask represents a task stored in the relay's queue.
type RelayTask struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Identity   string     `json:"identity"`
	Prompt     string     `json:"prompt"`
	Skill      string     `json:"skill,omitempty"`
	Agent      string     `json:"agent,omitempty"`
	Isolation  string     `json:"isolation,omitempty"`
	Status     string     `json:"status"` // pending, running, done, failed
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	WingID     string     `json:"wing_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
