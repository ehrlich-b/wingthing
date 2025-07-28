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

// OpenAIProvider implements the Provider interface for OpenAI's API
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewOpenAIProvider creates a new OpenAI provider
func NewOpenAIProvider(apiKey, baseURL string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// SupportsModel returns true if this provider supports the given model
func (p *OpenAIProvider) SupportsModel(model string) bool {
	// OpenAI model patterns
	return strings.HasPrefix(model, "gpt-") ||
		strings.HasPrefix(model, "o1-") ||
		model == "text-davinci-003" ||
		model == "text-davinci-002"
}

// Chat sends a conversation to OpenAI and returns the response
func (p *OpenAIProvider) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	// Convert to OpenAI format
	openaiReq := p.convertRequest(req)

	// Make HTTP request
	respBody, err := p.makeRequest(ctx, openaiReq)
	if err != nil {
		return nil, err
	}

	// Parse response
	var openaiResp openaiChatResponse
	if err := json.Unmarshal(respBody, &openaiResp); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	// Convert to our format
	return p.convertResponse(&openaiResp), nil
}

// OpenAI API types
type openaiChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Tools       []openaiTool    `json:"tools,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Temperature *float32        `json:"temperature,omitempty"`
	TopP        *float32        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
}

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiTool struct {
	Type     string             `json:"type"`
	Function openaiToolFunction `json:"function"`
}

type openaiToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiToolCallFunc `json:"function"`
}

type openaiToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiChatResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   openaiUsage    `json:"usage"`
}

type openaiChoice struct {
	Index        int           `json:"index"`
	Message      openaiMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// convertRequest converts our request format to OpenAI format
func (p *OpenAIProvider) convertRequest(req *ChatRequest) *openaiChatRequest {
	openaiReq := &openaiChatRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
	}

	// Convert messages
	for _, msg := range req.Messages {
		openaiReq.Messages = append(openaiReq.Messages, openaiMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Convert tools
	for _, tool := range req.Tools {
		openaiReq.Tools = append(openaiReq.Tools, openaiTool{
			Type: tool.Type,
			Function: openaiToolFunction{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			},
		})
	}

	return openaiReq
}

// convertResponse converts OpenAI response to our format
func (p *OpenAIProvider) convertResponse(resp *openaiChatResponse) *ChatResponse {
	if len(resp.Choices) == 0 {
		return &ChatResponse{
			Content:  "",
			Finished: true,
		}
	}

	choice := resp.Choices[0]
	result := &ChatResponse{
		Content:  choice.Message.Content,
		Finished: choice.FinishReason == "stop" || choice.FinishReason == "length",
		Usage: &TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}

	// Convert tool calls
	for _, toolCall := range choice.Message.ToolCalls {
		// Parse arguments JSON
		var args map[string]any
		if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
			// If parsing fails, put the raw string in a map
			args = map[string]any{
				"raw_arguments": toolCall.Function.Arguments,
			}
		}

		result.ToolCalls = append(result.ToolCalls, interfaces.ToolCall{
			ID:   toolCall.ID,
			Type: toolCall.Type,
			Function: interfaces.FunctionCall{
				Name:      toolCall.Function.Name,
				Arguments: args,
			},
		})
	}

	// If we have tool calls, we're not finished yet
	if len(result.ToolCalls) > 0 {
		result.Finished = false
	}

	return result
}

// makeRequest makes an HTTP request to OpenAI API
func (p *OpenAIProvider) makeRequest(ctx context.Context, req *openaiChatRequest) ([]byte, error) {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

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
		return nil, fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
