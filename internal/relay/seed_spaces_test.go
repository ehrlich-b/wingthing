package relay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/embedding"
)

type testEmbedder struct{}

func (t *testEmbedder) Name() string { return "test-4" }
func (t *testEmbedder) Dims() int    { return 4 }
func (t *testEmbedder) Embed(texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		v := make([]float32, 4)
		v[0] = float32(len(text))
		vecs[i] = v
	}
	return vecs, nil
}

func TestSeedSpacesFromIndex(t *testing.T) {
	s := testStore(t)

	yaml := `- slug: physics
  description: Fundamental physics
  centroid: >-
    quantum mechanics wave functions
- slug: math
  description: Pure and applied math
  centroid: >-
    algebra topology number theory
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "spaces.yaml")
	os.WriteFile(yamlPath, []byte(yaml), 0644)
	cacheDir := filepath.Join(dir, "cache")

	emb := &testEmbedder{}
	idx, err := embedding.LoadSpaceIndex(yamlPath, cacheDir, emb)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}

	n, err := SeedSpacesFromIndex(s, idx, "test-4")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}

	anchors, err := s.ListAnchors()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(anchors) != 2 {
		t.Errorf("anchors = %d, want 2", len(anchors))
	}

	got, err := s.GetSocialEmbeddingBySlug("physics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected physics anchor")
	}
	if got.Text != "Fundamental physics" {
		t.Errorf("text = %q, want 'Fundamental physics'", got.Text)
	}
	if got.Kind != "anchor" {
		t.Errorf("kind = %q, want anchor", got.Kind)
	}
	// Real embeddings, not zeros
	if len(got.Embedding) != 4*4 {
		t.Errorf("embedding size = %d, want 16", len(got.Embedding))
	}
	if len(got.Centroid512) != 4*4 {
		t.Errorf("centroid_512 size = %d, want 16", len(got.Centroid512))
	}
}

func TestSeedSpacesIdempotent(t *testing.T) {
	s := testStore(t)
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	emb := &testEmbedder{}

	yaml1 := `- slug: test
  description: Version 1
  centroid: >-
    keywords v1
`
	yamlPath := filepath.Join(dir, "spaces.yaml")
	os.WriteFile(yamlPath, []byte(yaml1), 0644)
	os.RemoveAll(cacheDir)

	idx1, _ := embedding.LoadSpaceIndex(yamlPath, cacheDir, emb)
	SeedSpacesFromIndex(s, idx1, "test-4")

	// Re-seed with updated description
	yaml2 := `- slug: test
  description: Version 2
  centroid: >-
    keywords v2
`
	os.WriteFile(yamlPath, []byte(yaml2), 0644)
	os.RemoveAll(cacheDir)

	idx2, _ := embedding.LoadSpaceIndex(yamlPath, cacheDir, emb)
	SeedSpacesFromIndex(s, idx2, "test-4")

	anchors, err := s.ListAnchors()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(anchors) != 1 {
		t.Errorf("anchors = %d, want 1", len(anchors))
	}

	got, err := s.GetSocialEmbeddingBySlug("test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Text != "Version 2" {
		t.Errorf("text = %q, want 'Version 2'", got.Text)
	}
}

func TestSeedSpacesMissingEmbedder(t *testing.T) {
	s := testStore(t)
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "spaces.yaml")
	os.WriteFile(yamlPath, []byte(`- slug: a
  description: A
  centroid: alpha
`), 0644)

	emb := &testEmbedder{}
	idx, _ := embedding.LoadSpaceIndex(yamlPath, filepath.Join(dir, "cache"), emb)

	_, err := SeedSpacesFromIndex(s, idx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing embedder")
	}
}
