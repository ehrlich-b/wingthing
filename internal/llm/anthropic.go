package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/behrlich/wingthing/internal/interfaces"
)

// AnthropicProvider implements the Provider interface for Anthropic's API
type AnthropicProvider struct {
	apiKey string
	client *http.Client
}

// NewAnthropicProvider creates a new Anthropic provider
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// SupportsModel returns true if this provider supports the given model
func (p *AnthropicProvider) SupportsModel(model string) bool {
	// Anthropic model patterns
	return strings.HasPrefix(model, "claude-")
}

// Chat sends a conversation to Anthropic and returns the response
func (p *AnthropicProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	// Convert to Anthropic format
	anthropicReq := p.convertRequest(req)

	// Make HTTP request
	respBody, err := p.makeRequest(ctx, anthropicReq)
	if err != nil {
		return nil, err
	}

	// Parse response
	var anthropicResp anthropicResponse
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, fmt.Errorf("failed to parse Anthropic response: %w", err)
	}

	// Convert to our format
	return p.convertResponse(&anthropicResp), nil
}

// Anthropic API types
type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	System      string             `json:"system,omitempty"`
	Temperature *float32           `json:"temperature,omitempty"`
	TopP        *float32           `json:"top_p,omitempty"`
	StopSeq     []string           `json:"stop_sequences,omitempty"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // Can be string or array of content blocks
}

type anthropicContentBlock struct {
	Type       string               `json:"type"`
	Text       string               `json:"text,omitempty"`
	ToolUse    *anthropicToolUse    `json:"tool_use,omitempty"`
	ToolResult *anthropicToolResult `json:"tool_result,omitempty"`
}

type anthropicToolUse struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

type anthropicToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []anthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   string                  `json:"stop_reason"`
	StopSequence string                  `json:"stop_sequence"`
	Usage        anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// convertRequest converts our request format to Anthropic format
func (p *AnthropicProvider) convertRequest(req *ChatRequest) *anthropicRequest {
	anthropicReq := &anthropicRequest{
		Model:       req.Model,
		MaxTokens:   4096, // Default for Anthropic
		Temperature: req.Temperature,
		TopP:        req.TopP,
		StopSeq:     req.Stop,
	}

	if req.MaxTokens != nil {
		anthropicReq.MaxTokens = *req.MaxTokens
	}

	// Convert messages - Anthropic separates system messages
	var systemMsg string
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			if systemMsg != "" {
				systemMsg += "\n\n"
			}
			systemMsg += msg.Content
		} else {
			anthropicReq.Messages = append(anthropicReq.Messages, anthropicMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	if systemMsg != "" {
		anthropicReq.System = systemMsg
	}

	// Convert tools
	for _, tool := range req.Tools {
		anthropicReq.Tools = append(anthropicReq.Tools, anthropicTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		})
	}

	return anthropicReq
}

// convertResponse converts Anthropic response to our format
func (p *AnthropicProvider) convertResponse(resp *anthropicResponse) *ChatResponse {
	result := &ChatResponse{
		Finished: resp.StopReason == "end_turn" || resp.StopReason == "max_tokens",
		Usage: &TokenUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	// Process content blocks
	var textContent []string
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			textContent = append(textContent, block.Text)
		case "tool_use":
			if block.ToolUse != nil {
				result.ToolCalls = append(result.ToolCalls, interfaces.ToolCall{
					ID:   block.ToolUse.ID,
					Type: "function",
					Function: interfaces.FunctionCall{
						Name:      block.ToolUse.Name,
						Arguments: block.ToolUse.Input,
					},
				})
			}
		}
	}

	result.Content = strings.Join(textContent, "")

	// If we have tool calls, we're not finished yet
	if len(result.ToolCalls) > 0 {
		result.Finished = false
	}

	return result
}

// makeRequest makes an HTTP request to Anthropic API
func (p *AnthropicProvider) makeRequest(ctx context.Context, req *anthropicRequest) ([]byte, error) {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Anthropic API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
