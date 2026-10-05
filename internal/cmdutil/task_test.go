package cmdutil

import (
	"testing"
)

func TestGeneratedTaskIDsDoNotCollideWithinSecond(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		id := GenTaskID()
		if seen[id] {
			t.Fatalf("duplicate task ID %q", id)
		}
		seen[id] = true
	}
}
