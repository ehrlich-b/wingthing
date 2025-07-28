package agent

import "github.com/behrlich/wingthing/internal/interfaces"

type EventType string

const (
	EventTypePlan              EventType = "plan"
	EventTypeRunTool           EventType = "run_tool"
	EventTypeObservation       EventType = "observation"
	EventTypeFinal             EventType = "final"
	EventTypePermissionRequest EventType = "permission_request"
	EventTypeError             EventType = "error"
)

// Use Event type from interfaces package
type Event = interfaces.Event

type PermissionRequest struct {
	Tool        string         `json:"tool"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolExecution struct {
	Tool       string         `json:"tool"`
	Parameters map[string]any `json:"parameters"`
	Output     string         `json:"output"`
	Error      string         `json:"error,omitempty"`
}
