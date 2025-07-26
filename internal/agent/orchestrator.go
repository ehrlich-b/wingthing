package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/behrlich/wingthing/internal/tools"
)

type Orchestrator struct {
	toolRunner tools.Runner
	events     chan<- Event
	memory     *Memory
	permissions *PermissionEngine
}

func NewOrchestrator(toolRunner tools.Runner, events chan<- Event) *Orchestrator {
	return &Orchestrator{
		toolRunner:  toolRunner,
		events:      events,
		memory:      NewMemory(),
		permissions: NewPermissionEngine(),
	}
}

func (o *Orchestrator) ProcessPrompt(ctx context.Context, prompt string) error {
	// Emit planning event
	o.events <- Event{
		Type:    EventTypePlan,
		Content: fmt.Sprintf("Planning response to: %s", prompt),
	}

	// TODO: Replace with real LLM call
	time.Sleep(1 * time.Second)

	// Simulate tool execution request
	if prompt == "list files" {
		// Check permissions
		allowed, err := o.permissions.CheckPermission("bash", "ls", map[string]any{"command": "ls -la"})
		if err != nil {
			o.events <- Event{Type: EventTypeError, Content: err.Error()}
			return err
		}

		if !allowed {
			// Request permission
			o.events <- Event{
				Type:    EventTypePermissionRequest,
				Content: "The agent wants to run 'ls -la' to list files in the current directory.",
				Data: PermissionRequest{
					Tool:        "bash",
					Description: "List files in current directory",
					Parameters:  map[string]any{"command": "ls -la"},
				},
			}
			return nil
		}

		// Execute tool
		o.events <- Event{
			Type:    EventTypeRunTool,
			Content: "Running: ls -la",
		}

		result, err := o.toolRunner.Run(ctx, "bash", map[string]any{"command": "ls -la"})
		if err != nil {
			o.events <- Event{Type: EventTypeError, Content: err.Error()}
			return err
		}

		o.events <- Event{
			Type:    EventTypeObservation,
			Content: fmt.Sprintf("Command output: %s", result.Output),
		}
	}

	// Emit final response
	o.events <- Event{
		Type:    EventTypeFinal,
		Content: "I've processed your request. This is a stub implementation.",
	}

	return nil
}

func (o *Orchestrator) GrantPermission(tool, action string, params map[string]any, decision PermissionDecision) {
	o.permissions.GrantPermission(tool, action, params, decision)
}

func (o *Orchestrator) DenyPermission(tool, action string, params map[string]any, decision PermissionDecision) {
	o.permissions.DenyPermission(tool, action, params, decision)
}