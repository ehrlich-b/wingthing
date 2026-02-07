package embedding

// Embedder produces vector embeddings from text.
type Embedder interface {
	Embed(texts []string) ([][]float32, error)
	Dims() int
}
