package phonepair

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTerminalEmojiLabelsMatchBrowserVocabulary(t *testing.T) {
	source, err := os.ReadFile("../../web/src/pake/emoji.js")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`const labels = (\[.*\]);`).FindSubmatch(source)
	if len(match) != 2 {
		t.Fatal("browser labels missing")
	}
	var browser []string
	if err := json.Unmarshal(match[1], &browser); err != nil {
		t.Fatal(err)
	}
	if len(browser) != len(emojis) || len(emojiLabels) != len(emojis) {
		t.Fatal("confirmation vocabulary lengths differ")
	}
	for i, glyph := range emojis {
		if got := EmojiLabels(glyph); got != browser[i] {
			t.Fatalf("%s: terminal %q browser %q", glyph, got, browser[i])
		}
	}
	if got := EmojiLabels(strings.Join(emojis[:4], " ")); got != strings.Join(browser[:4], " · ") {
		t.Fatal("confirmation labels do not follow glyph order", got)
	}
}
