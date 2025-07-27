package llm

import (
	"context"
	"fmt"

	"github.com/behrlich/wingthing/internal/interfaces"
)

// Client represents a unified LLM client that can route to different providers
type Client struct {
	providers map[string]Provider
	config    *ClientConfig
}

// ClientConfig holds configuration for the LLM client
type ClientConfig struct {
	DefaultModel string
	APIKey       string
	BaseURL      string
}

// Provider defines the interface that each LLM provider adapter must implement
type Provider interface {
	// Chat sends a conversation to the provider and returns the response
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	
	// SupportsModel returns true if this provider supports the given model
	SupportsModel(model string) bool
}

// ChatRequest represents a request to an LLM provider
type ChatRequest struct {
	Model       string                  `json:"model"`
	Messages    []interfaces.Message    `json:"messages"`
	Tools       []Tool                  `json:"tools,omitempty"`
	MaxTokens   *int                    `json:"max_tokens,omitempty"`
	Temperature *float32                `json:"temperature,omitempty"`
	TopP        *float32                `json:"top_p,omitempty"`
	Stop        []string                `json:"stop,omitempty"`
}

// ChatResponse represents a response from an LLM provider
type ChatResponse struct {
	Content      string                 `json:"content"`
	ToolCalls    []interfaces.ToolCall  `json:"tool_calls,omitempty"`
	Finished     bool                   `json:"finished"`
	Usage        *TokenUsage            `json:"usage,omitempty"`
}

// Tool represents a tool that can be called by the LLM
type Tool struct {
	Type     string         `json:"type"`     // "function"
	Function FunctionSchema `json:"function"`
}

// FunctionSchema describes a function that can be called
type FunctionSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// TokenUsage represents token usage statistics
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// NewClient creates a new LLM client with the given configuration
func NewClient(config *ClientConfig) *Client {
	client := &Client{
		providers: make(map[string]Provider),
		config:    config,
	}
	
	// Register providers
	client.providers["openai"] = NewOpenAIProvider(config.APIKey, config.BaseURL)
	client.providers["anthropic"] = NewAnthropicProvider(config.APIKey)
	
	return client
}

// Chat implements the interfaces.LLMProvider interface
func (c *Client) Chat(ctx context.Context, messages []interfaces.Message) (*interfaces.LLMResponse, error) {
	// Determine model from config or use default
	model := c.config.DefaultModel
	if model == "" {
		model = "gpt-4o-mini" // Reasonable default
	}
	
	// Find provider for this model
	var provider Provider
	var providerName string
	for name, p := range c.providers {
		if p.SupportsModel(model) {
			provider = p
			providerName = name
			break
		}
	}
	
	if provider == nil {
		return nil, fmt.Errorf("no provider found for model: %s", model)
	}
	
	// Create request with tools
	req := &ChatRequest{
		Model:    model,
		Messages: messages,
		Tools:    c.getAvailableTools(),
	}
	
	// Call provider
	resp, err := provider.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("provider %s failed: %w", providerName, err)
	}
	
	// Convert to interface format
	return &interfaces.LLMResponse{
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
		Finished:  resp.Finished,
	}, nil
}

// getAvailableTools returns the tools available to the LLM
func (c *Client) getAvailableTools() []Tool {
	return []Tool{
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "cli",
				Description: "Execute a command line instruction",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{
							"type":        "string",
							"description": "The command to execute",
						},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "read_file",
				Description: "Read the contents of a file",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "The path to the file to read",
						},
					},
					"required": []string{"file_path"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "write_file",
				Description: "Write content to a file",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "The path to the file to write",
						},
						"content": map[string]interface{}{
							"type":        "string",
							"description": "The content to write to the file",
						},
					},
					"required": []string{"file_path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionSchema{
				Name:        "edit_file",
				Description: "Edit a file by replacing text",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type":        "string",
							"description": "The path to the file to edit",
						},
						"old_text": map[string]interface{}{
							"type":        "string",
							"description": "The text to replace",
						},
						"new_text": map[string]interface{}{
							"type":        "string",
							"description": "The replacement text",
						},
						"replace_all": map[string]interface{}{
							"type":        "boolean",
							"description": "Whether to replace all occurrences",
							"default":     false,
						},
					},
					"required": []string{"file_path", "old_text", "new_text"},
				},
			},
		},
	}
}