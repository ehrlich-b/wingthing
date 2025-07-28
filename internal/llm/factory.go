package llm

import (
	"time"

	"github.com/behrlich/wingthing/internal/interfaces"
)

// NewProvider creates an LLM provider based on configuration
func NewProvider(config *interfaces.Config, useDummy bool) interfaces.LLMProvider {
	if useDummy {
		return NewDummyProvider(500 * time.Millisecond)
	}

	// Create real LLM client
	clientConfig := &ClientConfig{
		DefaultModel: config.Model,
		APIKey:       config.APIKey,
		BaseURL:      config.BaseURL,
	}

	return NewClient(clientConfig)
}

// NewTestProvider creates a fast dummy provider for testing
func NewTestProvider() interfaces.LLMProvider {
	return NewDummyProvider(10 * time.Millisecond)
}
