package embedding

import (
	"encoding/binary"
	"math"
)

// Embedder produces vector embeddings from text.
type Embedder interface {
	Embed(texts []string) ([][]float32, error)
	Dims() int
	Name() string // unique key for caching, e.g. "openai-3small-512"
}

// VecAsBytes converts a float32 vector to a raw byte blob (for DB storage).
func VecAsBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// BytesAsVec converts a raw byte blob back to a float32 vector.
func BytesAsVec(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
