package egg

import (
	"encoding/json"
	"os"
	"testing"
)

// The browser and native journal share these synthetic wire records.
func TestCodexBrowserFixtureMatchesNativeContract(t *testing.T) {
	data, err := os.ReadFile("../../web/test/fixtures/codex-native.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture SessionView
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Events) != 6 {
		t.Fatalf("fixture events: %d", len(fixture.Events))
	}
	for _, expected := range fixture.Events {
		actual, ok, err := ParseCodexNativeEvent(fixture.ProviderSessionID, expected.Raw)
		if err != nil || !ok {
			t.Fatalf("event %d: %v %v", expected.Sequence, ok, err)
		}
		if actual.Event.Type != expected.Type || actual.Event.Role != expected.Role || actual.Event.Text != expected.Text || actual.Event.State != expected.State {
			t.Fatalf("event %d differs: %+v; expected %+v", expected.Sequence, actual.Event, expected)
		}
	}
}
