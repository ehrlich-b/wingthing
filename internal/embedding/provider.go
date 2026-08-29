package embedding

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const maxEmbeddingResponseBytes = 32 << 20

func readEmbeddingResponse(body io.Reader, contentLength int64) ([]byte, error) {
	if contentLength > maxEmbeddingResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxEmbeddingResponseBytes)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxEmbeddingResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxEmbeddingResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxEmbeddingResponseBytes)
	}
	return data, nil
}

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
	if err := resp.Body.Close(); err != nil {
		return false
	}
	return resp.StatusCode == http.StatusOK
}
