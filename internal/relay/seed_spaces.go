package relay

import (
	"fmt"

	"github.com/ehrlich-b/wingthing/internal/embedding"
)

// SeedSpacesFromIndex upserts anchors from a SpaceIndex with real embeddings.
// It writes centroids to both the legacy embedding_512 column and the new anchor_embeddings table.
func SeedSpacesFromIndex(store *RelayStore, idx *embedding.SpaceIndex, embedderName string) (int, error) {
	vecs := idx.Vecs(embedderName)
	if vecs == nil {
		return 0, fmt.Errorf("no vectors for embedder %q", embedderName)
	}

	for i, space := range idx.Spaces {
		vec := embedding.VecAsBytes(vecs[i])
		id := "anchor-" + space.Slug
		cp := space.Centroid

		existing, err := store.GetSocialEmbeddingBySlug(space.Slug)
		if err != nil {
			return 0, fmt.Errorf("check anchor %s: %w", space.Slug, err)
		}

		if existing != nil {
			_, err := store.db.Exec(
				`UPDATE social_embeddings SET text = ?, centerpoint = ?, embedding = ?, embedding_512 = ?, centroid_512 = ?, effective_512 = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
				space.Description, cp, vec, vec, vec, vec, existing.ID,
			)
			if err != nil {
				return 0, fmt.Errorf("update anchor %s: %w", space.Slug, err)
			}
			if err := store.UpsertAnchorEmbedding(existing.ID, embedderName, vec); err != nil {
				return 0, fmt.Errorf("upsert anchor embedding %s/%s: %w", space.Slug, embedderName, err)
			}
			continue
		}

		slug := space.Slug
		e := &SocialEmbedding{
			ID:           id,
			UserID:       "system",
			Text:         space.Description,
			Centerpoint:  &cp,
			Slug:         &slug,
			Embedding:    vec,
			Embedding512: vec,
			Centroid512:  vec,
			Effective512: vec,
			Kind:         "anchor",
			Visible:      true,
			Mass:         1,
			DecayedMass:  1.0,
		}
		if err := store.CreateSocialEmbedding(e); err != nil {
			return 0, fmt.Errorf("seed anchor %s: %w", space.Slug, err)
		}
		if err := store.UpsertAnchorEmbedding(id, embedderName, vec); err != nil {
			return 0, fmt.Errorf("upsert anchor embedding %s/%s: %w", space.Slug, embedderName, err)
		}
	}

	return len(idx.Spaces), nil
}
