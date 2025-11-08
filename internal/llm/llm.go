package llm

import (
	"context"
)

// Provider defines the interface for LLM providers
type Provider interface {
	// Chat sends a message and gets a response
	Chat(ctx context.Context, messages []Message) (string, error)

	// Name returns the provider name
	Name() string
}

// Message represents a chat message
type Message struct {
	Role    string // "system", "user", "assistant"
	Content string
}

// NewProvider creates a new LLM provider based on config
func NewProvider(providerType, apiKey, model string) (Provider, error) {
	// TODO: Implement provider factories
	// For now, return a stub
	return &stubProvider{}, nil
}

// stubProvider is a placeholder implementation
type stubProvider struct{}

func (s *stubProvider) Chat(ctx context.Context, messages []Message) (string, error) {
	// TODO: Implement actual LLM calls
	return "I'm a placeholder LLM response. Integration coming soon!", nil
}

func (s *stubProvider) Name() string {
	return "stub"
}
