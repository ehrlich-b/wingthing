package relay

import (
	"fmt"
	"sort"
	"time"

	"github.com/ehrlich-b/wingthing/internal/embedding"
	"github.com/google/uuid"
)

type PostParams struct {
	UserID      string
	Text        string
	Title       string
	Link        string
	Mass        int
	PublishedAt *time.Time
}

// CreatePost embeds text, assigns to anchors by cosine similarity, and stores the post.
// URL dedup: if Link is non-empty and already exists, returns the existing post (no duplicate).
func CreatePost(store *RelayStore, emb embedding.Embedder, p PostParams) (*SocialEmbedding, error) {
	if p.Mass < 1 {
		p.Mass = 1
	}

	// URL dedup
	if p.Link != "" {
		existing, err := store.GetSocialEmbeddingByLink(p.Link)
		if err != nil {
			return nil, fmt.Errorf("check link dedup: %w", err)
		}
		if existing != nil {
			return existing, nil
		}
	}

	// Embed the text
	vecs, err := emb.Embed([]string{p.Text})
	if err != nil {
		return nil, fmt.Errorf("embed text: %w", err)
	}
	vec := vecs[0]
	vecBytes := embedding.VecAsBytes(vec)

	// List all anchors and compute cosine similarity
	anchors, err := store.ListAnchors()
	if err != nil {
		return nil, fmt.Errorf("list anchors: %w", err)
	}

	type anchorMatch struct {
		ID         string
		Similarity float32
	}
	var matches []anchorMatch
	for _, a := range anchors {
		if len(a.Embedding512) == 0 {
			continue
		}
		anchorVec := embedding.BytesAsVec(a.Embedding512)
		sim := embedding.Cosine(vec, anchorVec)
		matches = append(matches, anchorMatch{ID: a.ID, Similarity: sim})
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Similarity > matches[j].Similarity
	})

	// Swallow if best match < 0.25
	swallowed := len(matches) == 0 || matches[0].Similarity < 0.25

	post := &SocialEmbedding{
		ID:           uuid.New().String(),
		UserID:       p.UserID,
		Text:         p.Text,
		Embedding:    vecBytes,
		Embedding512: vecBytes,
		Kind:         "post",
		Visible:      !swallowed,
		Mass:         p.Mass,
		DecayedMass:  float64(p.Mass),
		Swallowed:    swallowed,
	}
	if p.Title != "" {
		post.Title = &p.Title
	}
	if p.Link != "" {
		post.Link = &p.Link
	}
	if p.PublishedAt != nil {
		post.PublishedAt = p.PublishedAt
	}

	if err := store.CreateSocialEmbedding(post); err != nil {
		return nil, fmt.Errorf("create post: %w", err)
	}

	// Assign to top anchors above 0.40 threshold (max 2)
	var assignments []PostAnchor
	for i, m := range matches {
		if i >= 2 || m.Similarity < 0.40 {
			break
		}
		assignments = append(assignments, PostAnchor{
			PostID:     post.ID,
			AnchorID:   m.ID,
			Similarity: float64(m.Similarity),
		})
	}
	if len(assignments) > 0 {
		if err := store.AssignPostAnchors(post.ID, assignments); err != nil {
			return nil, fmt.Errorf("assign anchors: %w", err)
		}
	}

	return post, nil
}
