package embedding

import "sort"

const (
	ThresholdAssign   = float32(0.40) // post lands in anchor's feed
	ThresholdFrontier = float32(0.25) // near-miss zone
	ThresholdSwallow  = float32(0.25) // below this = spam
	MaxAssignments    = 2             // top-2 anchors
	ProximityBoost    = float32(0.05) // pro user additive bonus
)

// Assignment represents a post's assignment to an anchor.
type Assignment struct {
	AnchorIndex int
	Similarity  float32
}

// Assign returns the top-2 anchor assignments above ThresholdAssign.
// If isPro, adds ProximityBoost to all similarities before thresholding.
// Returns nil assignments and swallowed=true if all similarities are below ThresholdSwallow.
// Posts between ThresholdSwallow and ThresholdAssign are "frontier" (nil assignments, swallowed=false).
func Assign(postVec []float32, anchorVecs [][]float32, isPro bool) (assignments []Assignment, swallowed bool) {
	type scored struct {
		index int
		sim   float32
	}

	scores := make([]scored, len(anchorVecs))
	for i, av := range anchorVecs {
		sim := Cosine(postVec, av)
		if isPro {
			sim += ProximityBoost
		}
		scores[i] = scored{index: i, sim: sim}
	}

	sort.Slice(scores, func(i, j int) bool {
		return scores[i].sim > scores[j].sim
	})

	// Check if everything is below swallow threshold
	if len(scores) > 0 && scores[0].sim < ThresholdSwallow {
		return nil, true
	}

	for _, s := range scores {
		if s.sim < ThresholdAssign {
			break
		}
		assignments = append(assignments, Assignment{
			AnchorIndex: s.index,
			Similarity:  s.sim,
		})
		if len(assignments) >= MaxAssignments {
			break
		}
	}

	return assignments, false
}
