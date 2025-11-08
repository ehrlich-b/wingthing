package llm

import (
	"context"
	"fmt"

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

	resp, err := p.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    p.model,
		Messages: chatMessages,
	})
	if err != nil {
		return "", fmt.Errorf("OpenAI API call failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	return resp.Choices[0].Message.Content, nil
}

// Name returns the provider name
func (p *OpenAIProvider) Name() string {
	return "openai"
}
