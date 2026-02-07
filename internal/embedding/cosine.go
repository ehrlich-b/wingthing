package embedding

import (
	"math"
	"sort"
)

// Match represents a candidate's index and its cosine similarity to a query.
type Match struct {
	Index      int
	Similarity float32
}

// Cosine returns the cosine similarity between two vectors.
func Cosine(a, b []float32) float32 {
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB)))
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// Normalize performs L2 normalization on v in place and returns it.
func Normalize(v []float32) []float32 {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	norm := float32(math.Sqrt(float64(sum)))
	if norm == 0 {
		return v
	}
	for i := range v {
		v[i] /= norm
	}
	return v
}

// TopN returns the top-N candidates by cosine similarity to query.
func TopN(query []float32, candidates [][]float32, n int) []Match {
	matches := make([]Match, len(candidates))
	for i, c := range candidates {
		matches[i] = Match{Index: i, Similarity: Cosine(query, c)}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Similarity > matches[j].Similarity
	})
	if n > len(matches) {
		n = len(matches)
	}
	return matches[:n]
}

// Blend returns a weighted combination of two vectors, normalized.
func Blend(a, b []float32, wa, wb float32) []float32 {
	out := make([]float32, len(a))
	for i := range a {
		out[i] = wa*a[i] + wb*b[i]
	}
	return Normalize(out)
}
