package embedding

import (
	"os"
	"path/filepath"
	"testing"
)

// stubEmbedder returns deterministic vectors based on text length.
type stubEmbedder struct {
	name string
	dims int
}

func (s *stubEmbedder) Name() string { return s.name }
func (s *stubEmbedder) Dims() int    { return s.dims }
func (s *stubEmbedder) Embed(texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, s.dims)
		v[0] = float32(len(t)) // deterministic based on input
		vecs[i] = Normalize(v)
	}
	return vecs, nil
}

func TestLoadSpaces(t *testing.T) {
	yaml := `- slug: physics
  description: Fundamental forces
  centroid: >-
    quantum mechanics relativity
- slug: math
  description: Abstract structures
  centroid: >-
    algebra topology proofs
`
	path := filepath.Join(t.TempDir(), "spaces.yaml")
	os.WriteFile(path, []byte(yaml), 0644)

	spaces, err := LoadSpaces(path)
	if err != nil {
		t.Fatalf("LoadSpaces: %v", err)
	}
	if len(spaces) != 2 {
		t.Fatalf("want 2 spaces, got %d", len(spaces))
	}
	if spaces[0].Slug != "physics" {
		t.Errorf("slug = %q, want physics", spaces[0].Slug)
	}
	if spaces[1].Description != "Abstract structures" {
		t.Errorf("description = %q", spaces[1].Description)
	}
}

func TestLoadSpaceIndexMultiEmbedder(t *testing.T) {
	yaml := `- slug: a
  description: A
  centroid: >-
    alpha bravo charlie
- slug: b
  description: B
  centroid: >-
    delta echo foxtrot golf
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "spaces.yaml")
	os.WriteFile(yamlPath, []byte(yaml), 0644)
	cacheDir := filepath.Join(dir, "cache")

	emb1 := &stubEmbedder{name: "stub-4", dims: 4}
	emb2 := &stubEmbedder{name: "stub-8", dims: 8}

	idx, err := LoadSpaceIndex(yamlPath, cacheDir, emb1, emb2)
	if err != nil {
		t.Fatalf("LoadSpaceIndex: %v", err)
	}

	if len(idx.Spaces) != 2 {
		t.Fatalf("want 2 spaces, got %d", len(idx.Spaces))
	}

	// Both embedders should have vectors
	names := idx.EmbedderNames()
	if len(names) != 2 {
		t.Fatalf("want 2 embedder names, got %d", len(names))
	}

	v1 := idx.Vecs("stub-4")
	if len(v1) != 2 {
		t.Fatalf("stub-4: want 2 vecs, got %d", len(v1))
	}
	if len(v1[0]) != 4 {
		t.Errorf("stub-4: want 4 dims, got %d", len(v1[0]))
	}

	v2 := idx.Vecs("stub-8")
	if len(v2) != 2 {
		t.Fatalf("stub-8: want 2 vecs, got %d", len(v2))
	}
	if len(v2[0]) != 8 {
		t.Errorf("stub-8: want 8 dims, got %d", len(v2[0]))
	}

	// Lookup by slug
	space, vec := idx.Lookup("b", "stub-4")
	if space == nil {
		t.Fatal("lookup b: want space, got nil")
	}
	if space.Description != "B" {
		t.Errorf("lookup b: description = %q", space.Description)
	}
	if len(vec) != 4 {
		t.Errorf("lookup b: want 4 dims, got %d", len(vec))
	}

	// Missing slug
	space, _ = idx.Lookup("nonexistent", "stub-4")
	if space != nil {
		t.Errorf("lookup nonexistent: want nil, got %v", space)
	}

	// Missing embedder
	_, vec = idx.Lookup("a", "nonexistent")
	if vec != nil {
		t.Errorf("lookup missing embedder: want nil vec, got %v", vec)
	}
}

func TestSpaceIndexCache(t *testing.T) {
	yaml := `- slug: x
  description: X
  centroid: >-
    xray yankee zulu
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "spaces.yaml")
	os.WriteFile(yamlPath, []byte(yaml), 0644)
	cacheDir := filepath.Join(dir, "cache")

	emb := &stubEmbedder{name: "stub-4", dims: 4}

	// First load — embeds and caches
	idx1, err := LoadSpaceIndex(yamlPath, cacheDir, emb)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Cache file should exist
	cachePath := filepath.Join(cacheDir, "stub-4.bin")
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}

	// Second load — should use cache
	idx2, err := LoadSpaceIndex(yamlPath, cacheDir, emb)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}

	v1 := idx1.Vecs("stub-4")[0]
	v2 := idx2.Vecs("stub-4")[0]
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("cache mismatch at dim %d: %f vs %f", i, v1[i], v2[i])
		}
	}
}

func TestSpaceIndexCacheInvalidated(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "spaces.yaml")
	cacheDir := filepath.Join(dir, "cache")
	emb := &stubEmbedder{name: "stub-4", dims: 4}

	// Load with 1 space
	os.WriteFile(yamlPath, []byte(`- slug: a
  description: A
  centroid: >-
    alpha
`), 0644)
	_, err := LoadSpaceIndex(yamlPath, cacheDir, emb)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}

	// Change to 2 spaces — cache should be invalidated (count mismatch)
	os.WriteFile(yamlPath, []byte(`- slug: a
  description: A
  centroid: >-
    alpha
- slug: b
  description: B
  centroid: >-
    bravo
`), 0644)
	idx, err := LoadSpaceIndex(yamlPath, cacheDir, emb)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(idx.Spaces) != 2 {
		t.Fatalf("want 2 spaces after invalidation, got %d", len(idx.Spaces))
	}
	if len(idx.Vecs("stub-4")) != 2 {
		t.Fatalf("want 2 vecs after invalidation, got %d", len(idx.Vecs("stub-4")))
	}
}
