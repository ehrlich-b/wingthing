package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/ehrlich-b/wingthing/internal/logger"
	"github.com/sashabaranov/go-openai"
)

// OpenAIProvider implements the Provider interface for OpenAI
type OpenAIProvider struct {
	client *openai.Client
	model  string
}

// NewOpenAIProvider creates a new OpenAI provider
func NewOpenAIProvider(apiKey, model string) *OpenAIProvider {
	return &OpenAIProvider{
		client: openai.NewClient(apiKey),
		model:  model,
	}
}

// Chat sends messages to OpenAI and returns the response
func (p *OpenAIProvider) Chat(ctx context.Context, messages []Message) (string, error) {
	// Convert our Message type to OpenAI's ChatCompletionMessage
	chatMessages := make([]openai.ChatCompletionMessage, len(messages))
	for i, msg := range messages {
		chatMessages[i] = openai.ChatCompletionMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	logger.Debug("OpenAI API request",
		"model", p.model,
		"num_messages", len(messages))

	start := time.Now()

	resp, err := p.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    p.model,
		Messages: chatMessages,
	})

	duration := time.Since(start)

	if err != nil {
		logger.Error("OpenAI API call failed",
			"error", err,
			"duration", duration,
			"model", p.model)
		return "", fmt.Errorf("OpenAI API call failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		logger.Error("No response from OpenAI",
			"duration", duration,
			"model", p.model)
		return "", fmt.Errorf("no response from OpenAI")
	}

	response := resp.Choices[0].Message.Content

	logger.Debug("OpenAI API response",
		"model", p.model,
		"duration", duration,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"total_tokens", resp.Usage.TotalTokens,
		"response_length", len(response))

	logger.Debug("OpenAI response content",
		"content", response)

	return response, nil
}

// Name returns the provider name
func (p *OpenAIProvider) Name() string {
	return "openai"
}
