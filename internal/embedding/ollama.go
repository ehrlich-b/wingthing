package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	ollamaDefaultModel   = "mxbai-embed-large"
	ollamaDefaultBaseURL = "http://localhost:11434"
	ollamaDims           = 512
)

type Ollama struct {
	model   string
	baseURL string
	client  *http.Client
}

func NewOllama(model, baseURL string) *Ollama {
	if model == "" {
		model = ollamaDefaultModel
	}
	if baseURL == "" {
		baseURL = ollamaDefaultBaseURL
	}
	return &Ollama{
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *Ollama) Dims() int    { return ollamaDims }
func (o *Ollama) Name() string { return "ollama-" + o.model + "-512" }

func (o *Ollama) Embed(texts []string) ([][]float32, error) {
	body, err := json.Marshal(ollamaRequest{
		Model: o.model,
		Input: texts,
	})
	if err != nil {
		return nil, fmt.Errorf("embedding: marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", o.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embedding: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding: ollama returned %d: %s", resp.StatusCode, respBody)
	}

	var result ollamaResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("embedding: unmarshal response: %w", err)
	}

	vecs := make([][]float32, len(result.Embeddings))
	for i, emb := range result.Embeddings {
		if len(emb) > ollamaDims {
			emb = emb[:ollamaDims]
		}
		vecs[i] = emb
	}
	return vecs, nil
}

type ollamaRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}
