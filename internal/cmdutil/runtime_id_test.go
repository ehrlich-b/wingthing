package cmdutil

import (
	"regexp"
	"testing"
)

func TestNewRuntimeIDHasSixtyFourBitsOfReadableEntropy(t *testing.T) {
	want := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := NewRuntimeID()
		if !want.MatchString(id) {
			t.Fatalf("runtime ID %q is not 16 lowercase hex characters", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate runtime ID %q", id)
		}
		seen[id] = struct{}{}
	}
}
