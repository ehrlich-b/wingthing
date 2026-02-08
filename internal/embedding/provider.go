package embedding

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// NewFromProvider constructs an Embedder by provider name.
// "auto" (default) tries ollama first, falls back to openai.
// "ollama": model and baseURL are optional (defaults apply).
// "openai": reads OPENAI_API_KEY from environment.
func NewFromProvider(provider, model, baseURL string) (Embedder, error) {
	switch provider {
	case "auto", "":
		if ollamaReachable(baseURL) {
			return NewOllama(model, baseURL), nil
		}
		if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			return NewOpenAI(key), nil
		}
		return nil, fmt.Errorf("no embedder available — install ollama or set OPENAI_API_KEY")
	case "ollama":
		return NewOllama(model, baseURL), nil
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY not set")
		}
		return NewOpenAI(key), nil
	default:
		return nil, fmt.Errorf("unknown embedder provider %q (available: auto, ollama, openai)", provider)
	}
}

func ollamaReachable(baseURL string) bool {
	if baseURL == "" {
		baseURL = ollamaDefaultBaseURL
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
