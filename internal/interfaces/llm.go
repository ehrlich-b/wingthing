package interfaces

import "context"

// LLMResponse represents the response from an LLM
type LLMResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Finished  bool       `json:"finished"`
}

// ToolCall represents a tool the LLM wants to call
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall represents a function call request
type FunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// LLMProvider defines the interface for interacting with language models
type LLMProvider interface {
	// Chat sends a conversation to the LLM and returns the response
	Chat(ctx context.Context, messages []Message) (*LLMResponse, error)

	// Stream sends a conversation and streams the response (for future use)
	// Stream(ctx context.Context, messages []Message) (<-chan *LLMResponse, error)
}
