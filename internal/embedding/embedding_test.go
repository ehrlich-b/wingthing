package embedding

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- Cosine ---

func TestCosineIdentical(t *testing.T) {
	v := []float32{1, 2, 3}
	got := Cosine(v, v)
	if !approx(got, 1.0) {
		t.Fatalf("identical vectors: want 1.0, got %f", got)
	}
}

func TestCosineOrthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	got := Cosine(a, b)
	if !approx(got, 0.0) {
		t.Fatalf("orthogonal vectors: want 0.0, got %f", got)
	}
}

func TestCosineOpposite(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	got := Cosine(a, b)
	if !approx(got, -1.0) {
		t.Fatalf("opposite vectors: want -1.0, got %f", got)
	}
}

func TestCosineZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	got := Cosine(a, b)
	if got != 0 {
		t.Fatalf("zero vector: want 0.0, got %f", got)
	}
}

// --- Normalize ---

func TestNormalize(t *testing.T) {
	v := []float32{3, 4}
	Normalize(v)
	length := float32(math.Sqrt(float64(v[0]*v[0] + v[1]*v[1])))
	if !approx(length, 1.0) {
		t.Fatalf("normalize: want unit length, got %f", length)
	}
	if !approx(v[0], 0.6) || !approx(v[1], 0.8) {
		t.Fatalf("normalize: want [0.6 0.8], got [%f %f]", v[0], v[1])
	}
}

func TestNormalizeZero(t *testing.T) {
	v := []float32{0, 0, 0}
	got := Normalize(v)
	for i, x := range got {
		if x != 0 {
			t.Fatalf("normalize zero: index %d want 0, got %f", i, x)
		}
	}
}

// --- TopN ---

func TestTopN(t *testing.T) {
	query := []float32{1, 0, 0}
	candidates := [][]float32{
		{0, 1, 0},  // orthogonal
		{1, 0, 0},  // identical
		{0.5, 0.5, 0}, // partial
		{-1, 0, 0}, // opposite
	}
	got := TopN(query, candidates, 2)
	if len(got) != 2 {
		t.Fatalf("topn: want 2 results, got %d", len(got))
	}
	if got[0].Index != 1 {
		t.Fatalf("topn: want index 1 first, got %d", got[0].Index)
	}
	if got[1].Index != 2 {
		t.Fatalf("topn: want index 2 second, got %d", got[1].Index)
	}
	if got[0].Similarity <= got[1].Similarity {
		t.Fatalf("topn: results not in descending order")
	}
}

func TestTopNMoreThanCandidates(t *testing.T) {
	query := []float32{1, 0}
	candidates := [][]float32{{1, 0}}
	got := TopN(query, candidates, 5)
	if len(got) != 1 {
		t.Fatalf("topn: want 1 result, got %d", len(got))
	}
}

// --- Blend ---

func TestBlendEqual(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	got := Blend(a, b, 0.5, 0.5)
	// 50/50 blend of [1,0] and [0,1] = [0.5, 0.5], normalized = [0.707, 0.707]
	expected := float32(1.0 / math.Sqrt(2))
	if !approx(got[0], expected) || !approx(got[1], expected) {
		t.Fatalf("blend: want [%f %f], got [%f %f]", expected, expected, got[0], got[1])
	}
}

func TestBlendWeighted(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	got := Blend(a, b, 0.8, 0.2)
	// Should lean toward a
	if got[0] <= got[1] {
		t.Fatalf("blend weighted: expected a-component > b-component, got [%f %f]", got[0], got[1])
	}
	// Should be normalized
	length := float32(math.Sqrt(float64(got[0]*got[0] + got[1]*got[1])))
	if !approx(length, 1.0) {
		t.Fatalf("blend: want unit length, got %f", length)
	}
}

// --- EffectiveAnchor ---

func TestEffectiveAnchorWithCentroid(t *testing.T) {
	static := []float32{1, 0}
	centroid := []float32{0, 1}
	got := EffectiveAnchor(static, centroid)
	expected := float32(1.0 / math.Sqrt(2))
	if !approx(got[0], expected) || !approx(got[1], expected) {
		t.Fatalf("effective anchor: want [%f %f], got [%f %f]", expected, expected, got[0], got[1])
	}
}

func TestEffectiveAnchorNilCentroid(t *testing.T) {
	static := []float32{1, 0, 0}
	got := EffectiveAnchor(static, nil)
	if &got[0] != &static[0] {
		t.Fatalf("effective anchor nil centroid: should return static slice directly")
	}
}

// --- Assign ---

func TestAssignAboveThreshold(t *testing.T) {
	// Post very similar to anchor 0
	post := Normalize([]float32{1, 0.1, 0})
	anchors := [][]float32{
		Normalize([]float32{1, 0, 0}),
		Normalize([]float32{0, 1, 0}),
		Normalize([]float32{0, 0, 1}),
	}
	assignments, swallowed := Assign(post, anchors, false)
	if swallowed {
		t.Fatal("assign: should not be swallowed")
	}
	if len(assignments) == 0 {
		t.Fatal("assign: expected at least one assignment")
	}
	if assignments[0].AnchorIndex != 0 {
		t.Fatalf("assign: expected anchor 0, got %d", assignments[0].AnchorIndex)
	}
	if assignments[0].Similarity < ThresholdAssign {
		t.Fatalf("assign: similarity %f below threshold", assignments[0].Similarity)
	}
}

func TestAssignSwallowed(t *testing.T) {
	// Post orthogonal to all anchors
	post := Normalize([]float32{0, 0, 1})
	anchors := [][]float32{
		Normalize([]float32{1, 0, 0}),
		Normalize([]float32{0, 1, 0}),
	}
	assignments, swallowed := Assign(post, anchors, false)
	if !swallowed {
		t.Fatal("assign: should be swallowed")
	}
	if assignments != nil {
		t.Fatalf("assign: swallowed should have nil assignments, got %v", assignments)
	}
}

func TestAssignFrontier(t *testing.T) {
	// Construct vectors with cosine similarity ~0.33 (above swallow 0.25, below assign 0.40)
	post := Normalize([]float32{1, 0, 0, 0})
	anchors := [][]float32{
		Normalize([]float32{0.33, 0.94, 0, 0}),
	}
	sim := Cosine(post, anchors[0])
	if sim >= ThresholdAssign || sim < ThresholdSwallow {
		t.Skipf("test vector sim %f not in frontier zone, adjust vectors", sim)
	}
	assignments, swallowed := Assign(post, anchors, false)
	if swallowed {
		t.Fatal("assign frontier: should not be swallowed")
	}
	if assignments != nil {
		t.Fatal("assign frontier: should have nil assignments")
	}
}

func TestAssignProBoost(t *testing.T) {
	// Construct vectors with cosine sim ~0.38 (below 0.40 assign, but 0.38+0.05=0.43 with pro)
	post := Normalize([]float32{1, 0, 0, 0})
	anchors := [][]float32{
		Normalize([]float32{0.38, 0.925, 0, 0}),
	}
	baseSim := Cosine(post, anchors[0])
	if baseSim >= ThresholdAssign || baseSim < ThresholdAssign-ProximityBoost {
		t.Skipf("base sim %f not in testable range for pro boost", baseSim)
	}

	base, _ := Assign(post, anchors, false)
	pro, _ := Assign(post, anchors, true)
	if len(base) > 0 {
		t.Fatal("assign pro: base should not assign")
	}
	if len(pro) == 0 {
		t.Fatal("assign pro: pro should assign")
	}
}

func TestAssignMaxTwo(t *testing.T) {
	// Post very similar to 3 anchors
	post := Normalize([]float32{1, 1, 1})
	anchors := [][]float32{
		Normalize([]float32{1, 1, 1}),
		Normalize([]float32{1, 1, 0.9}),
		Normalize([]float32{1, 0.9, 1}),
	}
	assignments, swallowed := Assign(post, anchors, false)
	if swallowed {
		t.Fatal("assign max: should not be swallowed")
	}
	if len(assignments) > MaxAssignments {
		t.Fatalf("assign max: got %d assignments, want at most %d", len(assignments), MaxAssignments)
	}
}

// --- OpenAI adapter (mock) ---

func TestOpenAIEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("openai: want POST, got %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("openai: want Bearer test-key, got %s", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("openai: want application/json, got %s", got)
		}

		var req openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("openai: decode request: %v", err)
		}
		if req.Model != openAIModel {
			t.Errorf("openai: want model %s, got %s", openAIModel, req.Model)
		}
		if req.Dimensions != openAIDims {
			t.Errorf("openai: want dims %d, got %d", openAIDims, req.Dimensions)
		}
		if len(req.Input) != 2 {
			t.Errorf("openai: want 2 inputs, got %d", len(req.Input))
		}

		// Return out of order to test index sorting
		resp := openAIResponse{
			Data: []openAIEmbedding{
				{Index: 1, Embedding: make([]float32, openAIDims)},
				{Index: 0, Embedding: make([]float32, openAIDims)},
			},
		}
		resp.Data[0].Embedding[0] = 0.2 // index 1
		resp.Data[1].Embedding[0] = 0.1 // index 0
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := NewOpenAI("test-key")
	o.client = srv.Client()
	// Override endpoint by replacing the client transport
	o.client.Transport = rewriteTransport{base: srv.Client().Transport, url: srv.URL}

	vecs, err := o.Embed([]string{"hello", "world"})
	if err != nil {
		t.Fatalf("openai embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("openai: want 2 vecs, got %d", len(vecs))
	}
	// After sorting by index: vecs[0] should have 0.1, vecs[1] should have 0.2
	if vecs[0][0] != 0.1 {
		t.Errorf("openai: vecs[0][0] want 0.1, got %f", vecs[0][0])
	}
	if vecs[1][0] != 0.2 {
		t.Errorf("openai: vecs[1][0] want 0.2, got %f", vecs[1][0])
	}
}

func TestOpenAIEmbedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	o := NewOpenAI("test-key")
	o.client = srv.Client()
	o.client.Transport = rewriteTransport{base: srv.Client().Transport, url: srv.URL}

	_, err := o.Embed([]string{"hello"})
	if err == nil {
		t.Fatal("openai: expected error on 429")
	}
}

// --- Ollama adapter (mock) ---

func TestOllamaEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("ollama: want POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("ollama: want /api/embed, got %s", r.URL.Path)
		}

		var req ollamaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("ollama: decode request: %v", err)
		}
		if req.Model != ollamaDefaultModel {
			t.Errorf("ollama: want model %s, got %s", ollamaDefaultModel, req.Model)
		}
		if len(req.Input) != 1 {
			t.Errorf("ollama: want 1 input, got %d", len(req.Input))
		}

		// Return 768-dim vector to test truncation
		vec := make([]float32, 768)
		vec[0] = 0.5
		vec[511] = 0.9
		vec[512] = 0.99 // should be truncated
		resp := ollamaResponse{Embeddings: [][]float32{vec}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := NewOllama("", srv.URL)
	vecs, err := o.Embed([]string{"hello"})
	if err != nil {
		t.Fatalf("ollama embed: %v", err)
	}
	if len(vecs) != 1 {
		t.Fatalf("ollama: want 1 vec, got %d", len(vecs))
	}
	if len(vecs[0]) != ollamaDims {
		t.Fatalf("ollama: want %d dims, got %d", ollamaDims, len(vecs[0]))
	}
	if vecs[0][0] != 0.5 {
		t.Errorf("ollama: vecs[0][0] want 0.5, got %f", vecs[0][0])
	}
	if vecs[0][511] != 0.9 {
		t.Errorf("ollama: vecs[0][511] want 0.9, got %f", vecs[0][511])
	}
}

func TestOllamaEmbedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`model not found`))
	}))
	defer srv.Close()

	o := NewOllama("bad-model", srv.URL)
	_, err := o.Embed([]string{"hello"})
	if err == nil {
		t.Fatal("ollama: expected error on 500")
	}
}

// --- helpers ---

func approx(a, b float32) bool {
	return math.Abs(float64(a-b)) < 1e-4
}

// rewriteTransport rewrites all requests to the test server URL.
type rewriteTransport struct {
	base http.RoundTripper
	url  string
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = t.url[len("http://"):]
	if t.base == nil {
		return http.DefaultTransport.RoundTrip(req)
	}
	return t.base.RoundTrip(req)
}
