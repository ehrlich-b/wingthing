package agent

type EventType string

const (
	EventTypePlan              EventType = "plan"
	EventTypeRunTool          EventType = "run_tool"
	EventTypeObservation      EventType = "observation"
	EventTypeFinal            EventType = "final"
	EventTypePermissionRequest EventType = "permission_request"
	EventTypeError            EventType = "error"
)

type Event struct {
	Type    EventType `json:"type"`
	Content string    `json:"content"`
	Data    any       `json:"data,omitempty"`
}

type PermissionRequest struct {
	Tool        string            `json:"tool"`
	Description string            `json:"description"`
	Parameters  map[string]any    `json:"parameters"`
}

type ToolExecution struct {
	Tool       string            `json:"tool"`
	Parameters map[string]any    `json:"parameters"`
	Output     string            `json:"output"`
	Error      string            `json:"error,omitempty"`
}