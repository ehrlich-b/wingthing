package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

const (
	openAIModel    = "text-embedding-3-small"
	openAIDims     = 512
	openAIEndpoint = "https://api.openai.com/v1/embeddings"
)

type OpenAI struct {
	apiKey string
	client *http.Client
}

func NewOpenAI(apiKey string) *OpenAI {
	return &OpenAI{
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *OpenAI) Dims() int    { return openAIDims }
func (o *OpenAI) Name() string { return "openai-3small-512" }

func (o *OpenAI) Embed(texts []string) ([][]float32, error) {
	body, err := json.Marshal(openAIRequest{
		Model:      openAIModel,
		Input:      texts,
		Dimensions: openAIDims,
	})
	if err != nil {
		return nil, fmt.Errorf("embedding: marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", openAIEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

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
		return nil, fmt.Errorf("embedding: openai returned %d: %s", resp.StatusCode, respBody)
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("embedding: unmarshal response: %w", err)
	}

	sort.Slice(result.Data, func(i, j int) bool {
		return result.Data[i].Index < result.Data[j].Index
	})

	vecs := make([][]float32, len(result.Data))
	for i, d := range result.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

type openAIRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

type openAIResponse struct {
	Data []openAIEmbedding `json:"data"`
}

type openAIEmbedding struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}
