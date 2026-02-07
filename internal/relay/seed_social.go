package relay

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type anchorDef struct {
	Slug        string `yaml:"slug"`
	Label       string `yaml:"label"`
	Description string `yaml:"description"`
	Centerpoint string `yaml:"centerpoint"`
}

type anchorsFile struct {
	Anchors []anchorDef `yaml:"anchors"`
}

// SeedAnchors reads anchors YAML and inserts/updates each as kind='anchor' in social_embeddings.
// Embeddings are stored as zero placeholders — caller must compute real embeddings separately.
func SeedAnchors(store *RelayStore, data []byte) (int, error) {
	var af anchorsFile
	if err := yaml.Unmarshal(data, &af); err != nil {
		return 0, fmt.Errorf("parse anchors yaml: %w", err)
	}

	placeholder := make([]byte, 512*4) // 512 float32 zeros

	for _, a := range af.Anchors {
		id := "anchor-" + a.Slug
		cp := a.Centerpoint

		existing, err := store.GetSocialEmbeddingBySlug(a.Slug)
		if err != nil {
			return 0, fmt.Errorf("check existing anchor %s: %w", a.Slug, err)
		}

		if existing != nil {
			// Update description and centerpoint, preserve embeddings
			_, err := store.db.Exec(
				`UPDATE social_embeddings SET text = ?, centerpoint = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
				a.Description, cp, existing.ID,
			)
			if err != nil {
				return 0, fmt.Errorf("update anchor %s: %w", a.Slug, err)
			}
			continue
		}

		slug := a.Slug
		e := &SocialEmbedding{
			ID:           id,
			UserID:       "system",
			Text:         a.Description,
			Centerpoint:  &cp,
			Slug:         &slug,
			Embedding:    placeholder,
			Embedding512: placeholder,
			Kind:         "anchor",
			Visible:      true,
			Mass:         1,
			DecayedMass:  1.0,
		}
		if err := store.CreateSocialEmbedding(e); err != nil {
			return 0, fmt.Errorf("seed anchor %s: %w", a.Slug, err)
		}
	}

	return len(af.Anchors), nil
}
