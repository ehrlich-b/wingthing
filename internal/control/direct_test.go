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
	sourceMessage := map[string]any{"message_id": "m1"}
	sourceSession := map[string]any{"id": "s1"}
	source := map[string]any{
		"session":  "s1",
		"message":  sourceMessage,
		"messages": []any{sourceMessage},
		"sessions": []any{sourceSession},
		"metadata": []any{[]any{map[string]any{"retained": "source"}}},
	}
	qualified := QualifyResult("wing-a", source)
	if qualified["wing_id"] != "wing-a" || source["wing_id"] != nil {
		t.Fatalf("qualified = %#v, source = %#v", qualified, source)
	}
	if sourceMessage["wing_id"] != nil || sourceSession["wing_id"] != nil {
		t.Fatalf("nested source was mutated: %#v", source)
	}
	if qualified["message"].(map[string]any)["wing_id"] != "wing-a" ||
		qualified["messages"].([]any)[0].(map[string]any)["wing_id"] != "wing-a" ||
		qualified["sessions"].([]any)[0].(map[string]any)["wing_id"] != "wing-a" {
		t.Fatalf("nested resources are not qualified: %#v", qualified)
	}
	qualified["metadata"].([]any)[0].([]any)[0].(map[string]any)["retained"] = "changed"
	if got := source["metadata"].([]any)[0].([]any)[0].(map[string]any)["retained"]; got != "source" {
		t.Fatalf("deeply nested result storage was shared with source: %v", got)
	}
}
