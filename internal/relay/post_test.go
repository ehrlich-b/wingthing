package relay

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/embedding"
)

type mockEmbedder struct {
	vec []float32
}

func (m *mockEmbedder) Embed(texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, len(m.vec))
		copy(v, m.vec)
		out[i] = v
	}
	return out, nil
}

func (m *mockEmbedder) Dims() int    { return len(m.vec) }
func (m *mockEmbedder) Name() string { return "mock" }

func makeAnchorWithVec(slug string, vec []float32) *SocialEmbedding {
	vecBytes := embedding.VecAsBytes(vec)
	s := slug
	return &SocialEmbedding{
		ID:           "anchor-" + slug,
		UserID:       "system",
		Text:         slug + " anchor",
		Slug:         &s,
		Embedding:    vecBytes,
		Embedding512: vecBytes,
		Centroid512:  vecBytes,
		Effective512: vecBytes,
		Kind:         "anchor",
		Visible:      true,
		Mass:         1,
		DecayedMass:  1.0,
	}
}

func norm(v []float32) []float32 {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	n := float32(math.Sqrt(float64(sum)))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func TestCreatePostAssignsAnchors(t *testing.T) {
	store := testStore(t)

	// Seed an anchor with a known vector
	anchorVec := norm([]float32{1, 0, 0, 0})
	anchor := makeAnchorWithVec("golang", anchorVec)
	if err := store.CreateSocialEmbedding(anchor); err != nil {
		t.Fatalf("create anchor: %v", err)
	}

	// Mock embedder returns vector similar to anchor (high cosine sim)
	postVec := norm([]float32{0.95, 0.31, 0, 0})
	emb := &mockEmbedder{vec: postVec}

	post, err := CreatePost(store, emb, PostParams{
		UserID: "user-1",
		Text:   "Go generics are neat",
		Mass:   5,
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	if post.ID == "" {
		t.Fatal("expected post ID")
	}
	if !post.Visible {
		t.Error("expected post to be visible (high similarity to anchor)")
	}
	if post.Mass != 5 {
		t.Errorf("mass = %d, want 5", post.Mass)
	}
	if post.Swallowed {
		t.Error("expected post not swallowed")
	}

	// Verify anchor assignment
	posts, err := store.ListPostsByAnchor(anchor.ID, "new", 10)
	if err != nil {
		t.Fatalf("list by anchor: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected 1 post assigned to anchor, got %d", len(posts))
	}
	if posts[0].ID != post.ID {
		t.Errorf("assigned post ID = %q, want %q", posts[0].ID, post.ID)
	}
}

func TestCreatePostSwallowedLowSimilarity(t *testing.T) {
	store := testStore(t)

	// Seed anchor with vector [1,0,0,0]
	anchorVec := norm([]float32{1, 0, 0, 0})
	anchor := makeAnchorWithVec("golang", anchorVec)
	if err := store.CreateSocialEmbedding(anchor); err != nil {
		t.Fatalf("create anchor: %v", err)
	}

	// Mock embedder returns orthogonal vector (cosine sim ~0)
	postVec := norm([]float32{0, 0, 0, 1})
	emb := &mockEmbedder{vec: postVec}

	post, err := CreatePost(store, emb, PostParams{
		UserID: "user-1",
		Text:   "completely unrelated content",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	if post.Visible {
		t.Error("expected post to be not visible (swallowed)")
	}
	if !post.Swallowed {
		t.Error("expected swallowed=true")
	}

	// Should not be assigned to any anchor
	posts, err := store.ListPostsByAnchor(anchor.ID, "new", 10)
	if err != nil {
		t.Fatalf("list by anchor: %v", err)
	}
	if len(posts) != 0 {
		t.Errorf("expected 0 posts assigned, got %d", len(posts))
	}
}

func TestCreatePostURLDedup(t *testing.T) {
	store := testStore(t)

	anchorVec := norm([]float32{1, 0, 0, 0})
	anchor := makeAnchorWithVec("golang", anchorVec)
	store.CreateSocialEmbedding(anchor)

	postVec := norm([]float32{0.95, 0.31, 0, 0})
	emb := &mockEmbedder{vec: postVec}

	post1, err := CreatePost(store, emb, PostParams{
		UserID: "user-1",
		Text:   "Go generics",
		Link:   "https://go.dev/blog/generics",
	})
	if err != nil {
		t.Fatalf("create post1: %v", err)
	}

	// Second post with same URL returns existing
	post2, err := CreatePost(store, emb, PostParams{
		UserID: "user-2",
		Text:   "different text same link",
		Link:   "https://go.dev/blog/generics",
	})
	if err != nil {
		t.Fatalf("create post2: %v", err)
	}

	if post2.ID != post1.ID {
		t.Errorf("expected dedup: post2.ID=%q, post1.ID=%q", post2.ID, post1.ID)
	}
}

func TestPostAPI(t *testing.T) {
	srv, ts := testServer(t)
	token, _ := createTestToken(t, srv.Store, "dev1")

	// Seed an anchor
	anchorVec := norm([]float32{1, 0, 0, 0})
	anchor := makeAnchorWithVec("golang", anchorVec)
	if err := srv.Store.CreateSocialEmbedding(anchor); err != nil {
		t.Fatalf("create anchor: %v", err)
	}

	// Set mock embedder on server
	postVec := norm([]float32{0.95, 0.31, 0, 0})
	srv.Embedder = &mockEmbedder{vec: postVec}

	// Post without auth — should fail
	resp, err := http.Post(ts.URL+"/api/post", "application/json",
		strings.NewReader(`{"text":"test post"}`))
	if err != nil {
		t.Fatalf("POST /api/post (unauthed): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthed status = %d, want 401", resp.StatusCode)
	}

	// Post with auth
	req, _ := http.NewRequest("POST", ts.URL+"/api/post",
		strings.NewReader(`{"text":"Go generics are neat","link":"https://go.dev/generics"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/post (authed): %v", err)
	}
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("authed post status = %d, want 200", resp.StatusCode)
	}
	if result["ok"] != true {
		t.Errorf("expected ok=true, got %v", result["ok"])
	}
	if result["post_id"] == nil || result["post_id"] == "" {
		t.Error("expected post_id in response")
	}
	if result["visible"] != true {
		t.Errorf("expected visible=true, got %v", result["visible"])
	}
}

func TestPostAPINoEmbedder(t *testing.T) {
	srv, ts := testServer(t)
	token, _ := createTestToken(t, srv.Store, "dev1")

	// Don't set embedder — should return 500
	req, _ := http.NewRequest("POST", ts.URL+"/api/post",
		strings.NewReader(`{"text":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Errorf("no-embedder status = %d, want 500", resp.StatusCode)
	}
}
