package localmcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionSearchReturnsAuthorizedExcerptWithoutChangingInventory(t *testing.T) {
	s := inventoryFixture(t, "sample-wing", "Studio Mac")
	result, err := s.ToolSessionSearch(t.Context(), json.RawMessage(`{"query":"fixed routing","include_remotes":false}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "match_excerpt") {
		t.Fatalf("missing authorized excerpt: %s", raw)
	}
	result, err = s.ToolSessionInventory(t.Context(), json.RawMessage(`{"query":"fixed routing"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(result)
	if strings.Contains(string(raw), "match_excerpt") {
		t.Fatal("general inventory leaked content")
	}
	s.tools = map[string]bool{"session_search": true, "session_inventory": true}
	result, err = s.ToolSessionSearch(t.Context(), json.RawMessage(`{"query":"fixed routing","include_remotes":false}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(result)
	if strings.Contains(string(raw), "match_excerpt") {
		t.Fatal("excerpt bypassed session_read capability")
	}
}

func TestSearchExcerptHonorsReadWorkspaceBounds(t *testing.T) {
	s := inventoryFixture(t, "sample-wing", "Studio Mac")
	s.enforcePathBounds = true
	s.allowedPaths = []string{"/different-workspace"}
	result, err := s.ToolSessionSearch(t.Context(), json.RawMessage(`{"query":"fixed routing","include_remotes":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(asInventory(t, result).Sessions) != 0 {
		t.Fatal("excerpt crossed the session_read workspace boundary")
	}
}
