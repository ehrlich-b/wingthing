package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/behrlich/wingthing/internal/interfaces"
)

// DummyProvider is a mock LLM provider for testing
type DummyProvider struct {
	delay time.Duration
}

// NewDummyProvider creates a new dummy LLM provider
func NewDummyProvider(delay time.Duration) *DummyProvider {
	return &DummyProvider{
		delay: delay,
	}
}

// Chat implements the LLMProvider interface with hardcoded responses
func (d *DummyProvider) Chat(ctx context.Context, messages []interfaces.Message) (*interfaces.LLMResponse, error) {
	// Simulate processing time
	time.Sleep(d.delay)

	// Get the last user message
	var lastUserMessage string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserMessage = strings.ToLower(messages[i].Content)
			break
		}
	}

	// Check for tool call requests
	if strings.Contains(lastUserMessage, "list files") || strings.Contains(lastUserMessage, "ls") {
		return &interfaces.LLMResponse{
			Content: "I'll help you list the files in the current directory.",
			ToolCalls: []interfaces.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "bash",
						Arguments: map[string]any{
							"command": "ls -la",
						},
					},
				},
			},
			Finished: false,
		}, nil
	}

	if strings.Contains(lastUserMessage, "read") && strings.Contains(lastUserMessage, "file") {
		return &interfaces.LLMResponse{
			Content: "I'll read the file for you.",
			ToolCalls: []interfaces.ToolCall{
				{
					ID:   "call_2",
					Type: "function",
					Function: interfaces.FunctionCall{
						Name: "read",
						Arguments: map[string]any{
							"path": "README.md",
						},
					},
				},
			},
			Finished: false,
		}, nil
	}

	if strings.Contains(lastUserMessage, "help") {
		return &interfaces.LLMResponse{
			Content: `I'm a dummy LLM provider for testing Wingthing. I can help with:
- Listing files (try "list files" or "ls")
- Reading files (try "read file")
- General conversation

This is a mock implementation to test the agent system.`,
			Finished: true,
		}, nil
	}

	if strings.Contains(lastUserMessage, "hello") || strings.Contains(lastUserMessage, "hi") {
		return &interfaces.LLMResponse{
			Content: "Hello! I'm a dummy AI assistant built into Wingthing. How can I help you today?",
			Finished: true,
		}, nil
	}

	// Handle tool results (when the user message contains tool output)
	if len(messages) >= 2 {
		prevMsg := messages[len(messages)-2]
		if prevMsg.Role == "assistant" && len(lastUserMessage) > 50 {
			return &interfaces.LLMResponse{
				Content: fmt.Sprintf("I can see the command executed successfully. The output shows: %s", 
					summarizeOutput(lastUserMessage)),
				Finished: true,
			}, nil
		}
	}

	// Default response
	return &interfaces.LLMResponse{
		Content: fmt.Sprintf("I understand you said: \"%s\". This is a dummy response from the mock LLM provider. Try asking me to 'list files' or 'read file' to see tool usage in action!", 
			lastUserMessage),
		Finished: true,
	}, nil
}

// summarizeOutput creates a brief summary of command output
func summarizeOutput(output string) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= 3 {
		return output
	}
	
	// Return first few lines with indication of more content
	summary := strings.Join(lines[:3], "\n")
	return fmt.Sprintf("%s\n... and %d more lines", summary, len(lines)-3)
}