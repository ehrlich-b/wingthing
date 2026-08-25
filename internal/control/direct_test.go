package control

import (
	"encoding/json"
	"testing"
)

func TestSplitWingTarget(t *testing.T) {
	wingID, forwarded, err := SplitWingTarget(json.RawMessage(`{"wing_id":"wing-a","agent":"codex"}`))
	if err != nil {
		t.Fatal(err)
	}
	if wingID != "wing-a" || string(forwarded) != `{"agent":"codex"}` {
		t.Fatalf("target = %q, forwarded = %s", wingID, forwarded)
	}
	if _, _, err := SplitWingTarget(json.RawMessage(`{"agent":"codex"}`)); err == nil {
		t.Fatal("missing wing_id was accepted")
	}
}

func TestQualifyResultDoesNotMutateSource(t *testing.T) {
	source := map[string]any{"session": "s1"}
	qualified := QualifyResult("wing-a", source)
	if qualified["wing_id"] != "wing-a" || source["wing_id"] != nil {
		t.Fatalf("qualified = %#v, source = %#v", qualified, source)
	}
}
