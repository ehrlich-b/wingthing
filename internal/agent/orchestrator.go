package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/behrlich/wingthing/internal/interfaces"
	"github.com/behrlich/wingthing/internal/tools"
)

type Orchestrator struct {
	toolRunner  tools.Runner
	events      chan<- Event
	memory      interfaces.MemoryManager
	permissions interfaces.PermissionChecker
	llmProvider interfaces.LLMProvider
	messages    []interfaces.Message
}

func NewOrchestrator(
	toolRunner tools.Runner,
	events chan<- Event,
	memory interfaces.MemoryManager,
	permissions interfaces.PermissionChecker,
	llmProvider interfaces.LLMProvider,
) *Orchestrator {
	return &Orchestrator{
		toolRunner:  toolRunner,
		events:      events,
		memory:      memory,
		permissions: permissions,
		llmProvider: llmProvider,
		messages:    make([]interfaces.Message, 0),
	}
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
			toolResults, err := o.handleToolCalls(ctx, response.ToolCalls)
			if err != nil {
				return err
			}

			// If we got permission requests, stop here and wait for user input
			if strings.Contains(toolResults, "PERMISSION_REQUESTED") {
				return nil
			}

			// Add tool results to conversation
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

func (o *Orchestrator) handleToolCalls(ctx context.Context, toolCalls []interfaces.ToolCall) (string, error) {
	var results []string

	for _, toolCall := range toolCalls {
		toolName := toolCall.Function.Name
		params := toolCall.Function.Arguments

		// Check permissions for tool usage
		allowed, err := o.permissions.CheckPermission(toolName, "execute", params)
		if err != nil {
			o.events <- Event{Type: string(EventTypeError), Content: err.Error()}
			return "", err
		}

		if !allowed {
			// Request permission
			o.events <- Event{
				Type:    string(EventTypePermissionRequest),
				Content: fmt.Sprintf("The agent wants to use the '%s' tool.", toolName),
				Data: PermissionRequest{
					Tool:        toolName,
					Description: fmt.Sprintf("Execute %s tool", toolName),
					Parameters:  params,
				},
			}
			return "PERMISSION_REQUESTED", nil
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

	return strings.Join(results, "\n"), nil
}

func (o *Orchestrator) GrantPermission(tool, action string, params map[string]any, decision PermissionDecision) {
	o.permissions.GrantPermission(tool, action, params, decision)
}

func (o *Orchestrator) DenyPermission(tool, action string, params map[string]any, decision PermissionDecision) {
	o.permissions.DenyPermission(tool, action, params, decision)
}
