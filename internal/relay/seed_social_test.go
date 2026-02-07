package relay

import (
	"os"
	"testing"
)

func TestSeedAnchorsSmall(t *testing.T) {
	s := testStore(t)

	yaml := []byte(`anchors:
  - slug: physics
    label: Physics
    description: Fundamental physics
    centerpoint: quantum mechanics wave functions
  - slug: math
    label: Mathematics
    description: Pure and applied math
    centerpoint: algebra topology number theory
  - slug: biology
    label: Biology
    description: Molecular biology and genetics
    centerpoint: DNA RNA protein gene expression
`)

	n, err := SeedAnchors(s, yaml)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}

	anchors, err := s.ListAnchors()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(anchors) != 3 {
		t.Errorf("anchors = %d, want 3", len(anchors))
	}

	// Verify fields
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
	if got.Centerpoint == nil || *got.Centerpoint != "quantum mechanics wave functions" {
		t.Errorf("centerpoint = %v, want 'quantum mechanics wave functions'", got.Centerpoint)
	}
	if got.Kind != "anchor" {
		t.Errorf("kind = %q, want anchor", got.Kind)
	}
	if got.UserID != "system" {
		t.Errorf("user_id = %q, want system", got.UserID)
	}
}

func TestSeedAnchorsIdempotent(t *testing.T) {
	s := testStore(t)

	yaml := []byte(`anchors:
  - slug: test
    label: Test
    description: Version 1
    centerpoint: keywords v1
`)

	n1, err := SeedAnchors(s, yaml)
	if err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if n1 != 1 {
		t.Errorf("count = %d, want 1", n1)
	}

	// Re-seed with updated description
	yaml2 := []byte(`anchors:
  - slug: test
    label: Test
    description: Version 2
    centerpoint: keywords v2
`)
	n2, err := SeedAnchors(s, yaml2)
	if err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	if n2 != 1 {
		t.Errorf("count = %d, want 1", n2)
	}

	// Should still be 1 anchor, not 2
	anchors, err := s.ListAnchors()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(anchors) != 1 {
		t.Errorf("anchors = %d, want 1", len(anchors))
	}

	// Description should be updated
	got, err := s.GetSocialEmbeddingBySlug("test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Text != "Version 2" {
		t.Errorf("text = %q, want 'Version 2'", got.Text)
	}
	if got.Centerpoint == nil || *got.Centerpoint != "keywords v2" {
		t.Errorf("centerpoint = %v, want 'keywords v2'", got.Centerpoint)
	}
}

func TestSeedAnchorsFullFile(t *testing.T) {
	s := testStore(t)

	data, err := os.ReadFile("../../anchors.yaml")
	if err != nil {
		t.Skipf("anchors.yaml not found: %v", err)
	}

	n, err := SeedAnchors(s, data)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n < 200 {
		t.Errorf("count = %d, want >= 200", n)
	}

	anchors, err := s.ListAnchors()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(anchors) < 200 {
		t.Errorf("anchors = %d, want >= 200", len(anchors))
	}
}
