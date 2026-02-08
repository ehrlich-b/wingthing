package embedding

// Embedder produces vector embeddings from text.
type Embedder interface {
	Embed(texts []string) ([][]float32, error)
	Dims() int
	Name() string // unique key for caching, e.g. "openai-3small-512"
}
