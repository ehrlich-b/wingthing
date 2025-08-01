package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/behrlich/wingthing/internal/interfaces"
	"github.com/behrlich/wingthing/internal/tools"
)

// ToolBatchResult represents the result of executing a batch of tool calls
type ToolBatchResult struct {
	Results           []string           // successful tool outputs
	PermissionRequest *PermissionRequest // nil if no permission needed
	Error             error              // any error that occurred
}

type Orchestrator struct {
	toolRunner  tools.Runner
	events      chan<- Event
	memory      interfaces.MemoryManager
	permissions interfaces.PermissionChecker
	llmProvider interfaces.LLMProvider
	messages    []interfaces.Message

	// Track pending tool execution for permission retry
	pendingToolCall *interfaces.ToolCall

	// Headless mode configuration
	headlessMode bool
	autoAccept   bool
}

func NewOrchestrator(
	toolRunner tools.Runner,
	events chan<- Event,
	memory interfaces.MemoryManager,
	permissions interfaces.PermissionChecker,
	llmProvider interfaces.LLMProvider,
) *Orchestrator {
	return &Orchestrator{
		toolRunner:   toolRunner,
		events:       events,
		memory:       memory,
		permissions:  permissions,
		llmProvider:  llmProvider,
		messages:     make([]interfaces.Message, 0),
		headlessMode: false,
		autoAccept:   false,
	}
}

// SetHeadlessMode configures the orchestrator for headless operation
func (o *Orchestrator) SetHeadlessMode(autoAccept bool) {
	o.headlessMode = true
	o.autoAccept = autoAccept
}

func (o *Orchestrator) ProcessPrompt(ctx context.Context, prompt string) error {
	// Add user message to conversation
	o.messages = append(o.messages, interfaces.Message{
		Role:    "user",
		Content: prompt,
	})

	// Start conversation loop
	return o.runConversationLoop(ctx)
}

func (o *Orchestrator) runConversationLoop(ctx context.Context) error {
	for {
		// Emit planning event
		o.events <- Event{
			Type:    string(EventTypePlan),
			Content: "Thinking about your request...",
		}

		// Get response from LLM
		response, err := o.llmProvider.Chat(ctx, o.messages)
		if err != nil {
			o.events <- Event{Type: string(EventTypeError), Content: err.Error()}
			return err
		}

		// Add assistant message to conversation
		o.messages = append(o.messages, interfaces.Message{
			Role:    "assistant",
			Content: response.Content,
		})

		// If LLM wants to use tools, handle them
		if len(response.ToolCalls) > 0 {
			batchResult := o.handleToolCalls(ctx, response.ToolCalls)

			// Check for errors
			if batchResult.Error != nil {
				return batchResult.Error
			}

			// If we got permission requests, stop here and wait for user input
			if batchResult.PermissionRequest != nil {
				return nil
			}

			// Add tool results to conversation
			toolResults := strings.Join(batchResult.Results, "\n")
			o.messages = append(o.messages, interfaces.Message{
				Role:    "user",
				Content: toolResults,
			})

			// Continue the loop to get LLM's response to tool results
			continue
		}

		// If finished, emit final response
		if response.Finished {
			o.events <- Event{
				Type:    string(EventTypeFinal),
				Content: response.Content,
			}
			return nil
		}

		// If not finished but no tool calls, something is wrong
		o.events <- Event{
			Type:    string(EventTypeError),
			Content: "LLM response was not finished but contained no tool calls",
		}
		return fmt.Errorf("invalid LLM response state")
	}
}

func (o *Orchestrator) handleToolCalls(ctx context.Context, toolCalls []interfaces.ToolCall) *ToolBatchResult {
	var results []string

	for _, toolCall := range toolCalls {
		toolName := toolCall.Function.Name
		params := toolCall.Function.Arguments

		// Check permissions for tool usage (only for tools that modify system state)
		needsPermission := o.toolNeedsPermission(toolName)
		var allowed bool
		var err error

		if needsPermission {
			allowed, err = o.permissions.CheckPermission(toolName, "execute", params)
			if err != nil {
				o.events <- Event{Type: string(EventTypeError), Content: err.Error()}
				return &ToolBatchResult{Error: err}
			}
		} else {
			allowed = true // Read-only operations are always allowed
		}

		if !allowed {
			// Handle permission request based on mode
			if o.headlessMode {
				// Create permission request for event emission
				permReq := &PermissionRequest{
					Tool:        toolName,
					Description: fmt.Sprintf("Allow wingthing to run this CLI command"),
					Parameters:  params,
				}

				// Emit permission request event
				o.events <- Event{
					Type:    string(EventTypePermissionRequest),
					Content: fmt.Sprintf("The agent wants to run a CLI command using the '%s' tool.", toolName),
					Data:    *permReq,
				}

				if o.autoAccept {
					// Auto-grant permission and continue execution
					o.permissions.GrantPermission(toolName, "execute", params, AllowOnce)
					allowed = true // Continue with tool execution
				} else {
					// Auto-deny permission and skip this tool
					o.permissions.DenyPermission(toolName, "execute", params, Deny)
					results = append(results, fmt.Sprintf("Tool %s denied: permission auto-denied in headless mode", toolName))
					continue
				}
			} else {
				// Interactive mode - save pending tool call for retry after permission
				o.pendingToolCall = &toolCall

				// Create permission request
				permReq := &PermissionRequest{
					Tool:        toolName,
					Description: fmt.Sprintf("Allow wingthing to run this CLI command"),
					Parameters:  params,
				}

				// Emit permission request event
				o.events <- Event{
					Type:    string(EventTypePermissionRequest),
					Content: fmt.Sprintf("The agent wants to run a CLI command using the '%s' tool.", toolName),
					Data:    *permReq,
				}

				return &ToolBatchResult{PermissionRequest: permReq}
			}
		}

		// Execute tool
		o.events <- Event{
			Type:    string(EventTypeRunTool),
			Content: fmt.Sprintf("Running: %s", toolName),
		}

		result, err := o.toolRunner.Run(ctx, toolName, params)
		if err != nil {
			o.events <- Event{Type: string(EventTypeError), Content: err.Error()}
			results = append(results, fmt.Sprintf("Tool %s failed: %s", toolName, err.Error()))
			continue
		}

		o.events <- Event{
			Type:    string(EventTypeObservation),
			Content: fmt.Sprintf("Tool output: %s", result.Output),
		}

		results = append(results, fmt.Sprintf("Tool %s output: %s", toolName, result.Output))
	}

	return &ToolBatchResult{Results: results}
}

func (o *Orchestrator) GrantPermission(tool, action string, params map[string]any, decision PermissionDecision) {
	o.permissions.GrantPermission(tool, action, params, decision)
}

func (o *Orchestrator) DenyPermission(tool, action string, params map[string]any, decision PermissionDecision) {
	o.permissions.DenyPermission(tool, action, params, decision)

	// Clear pending tool call since permission was denied
	if decision == Deny || decision == AlwaysDeny {
		o.pendingToolCall = nil
		o.events <- Event{
			Type:    string(EventTypeFinal),
			Content: "Permission denied. Tool execution cancelled.",
		}
	}
}

// RetryPendingTool attempts to execute the pending tool call after permission is granted
func (o *Orchestrator) RetryPendingTool(ctx context.Context) error {
	if o.pendingToolCall == nil {
		return fmt.Errorf("no pending tool call to retry")
	}

	toolCall := *o.pendingToolCall
	o.pendingToolCall = nil // Clear the pending call

	// Execute the tool directly (permission should now be granted)
	toolName := toolCall.Function.Name
	params := toolCall.Function.Arguments

	// Execute tool
	o.events <- Event{
		Type:    string(EventTypeRunTool),
		Content: fmt.Sprintf("Running: %s", toolName),
	}

	result, err := o.toolRunner.Run(ctx, toolName, params)
	if err != nil {
		o.events <- Event{Type: string(EventTypeError), Content: err.Error()}
		return err
	}

	// Check for tool execution error
	if result.Error != "" {
		o.events <- Event{Type: string(EventTypeError), Content: result.Error}
		return fmt.Errorf("tool execution failed: %s", result.Error)
	}

	// Send observation
	o.events <- Event{
		Type:    string(EventTypeObservation),
		Content: result.Output,
	}

	// Add tool results to conversation
	o.messages = append(o.messages, interfaces.Message{
		Role:    "user",
		Content: fmt.Sprintf("Tool %s executed successfully. Output: %s", toolName, result.Output),
	})

	// Get LLM response to tool results
	response, err := o.llmProvider.Chat(ctx, o.messages)
	if err != nil {
		o.events <- Event{Type: string(EventTypeError), Content: err.Error()}
		return err
	}

	// Add LLM response to conversation
	o.messages = append(o.messages, interfaces.Message{
		Role:    "assistant",
		Content: response.Content,
	})

	// Send final result
	o.events <- Event{
		Type:    string(EventTypeFinal),
		Content: response.Content,
	}

	return nil
}

// toolNeedsPermission determines if a tool requires user permission
func (o *Orchestrator) toolNeedsPermission(toolName string) bool {
	switch toolName {
	case "read_file":
		return false // Read-only operations don't need permission
	case "write_file", "edit_file":
		return true // File modifications need permission
	case "cli":
		return true // CLI commands need permission
	default:
		return true // Unknown tools need permission by default
	}
}
