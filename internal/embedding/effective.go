package embedding

// EffectiveAnchor computes normalize(0.5*static + 0.5*centroid).
// If centroid is nil (no posts yet), returns static as-is.
func EffectiveAnchor(static, centroid []float32) []float32 {
	if centroid == nil {
		return static
	}
	return Blend(static, centroid, 0.5, 0.5)
}
