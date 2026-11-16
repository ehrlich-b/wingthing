package phonepair

import (
	"encoding/hex"
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

func TestCheckEmojiMultiCodepointSharedSecretVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/emoji_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Invitation Invitation `json:"invitation"`
		Alice, Bob string
		Multi      []string `json:"multi_codepoint"`
		Vectors    []struct {
			Name   string
			Shared string `json:"shared_hex"`
			Key    string `json:"key_hex"`
			Emoji  string `json:"multi_emoji"`
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Multi) != 4 {
		t.Fatal("expected four synthetic graphemes")
	}
	// Both clients use byte indices into a 256-entry vocabulary. Substitute a
	// synthetic vocabulary to cover multi-codepoint entries without changing
	// any production index or the pairing protocol.
	original := emojis
	t.Cleanup(func() { emojis = original })
	emojis = make([]string, len(original))
	for i := range emojis {
		emojis[i] = fixture.Multi[i%len(fixture.Multi)]
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			key, err := hex.DecodeString(vector.Key)
			if err != nil {
				t.Fatal(err)
			}
			shared, err := hex.DecodeString(vector.Shared)
			if err != nil {
				t.Fatal(err)
			}
			if got := CheckEmoji(key); got != vector.Emoji {
				t.Fatalf("CheckEmoji: %q, want %q", got, vector.Emoji)
			}
			if got := EmojiFromShared(fixture.Invitation, shared, fixture.Alice, fixture.Bob); got != vector.Emoji {
				t.Fatalf("EmojiFromShared: %q, want %q", got, vector.Emoji)
			}
			t.Log(vector.Emoji)
		})
	}
}

func TestEmojiVocabularyMatchesSwift(t *testing.T) {
	source, err := os.ReadFile("../../mobile/ios/WingthingCore/Sources/WingthingCore/PairingEmoji.swift")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`let pairingEmoji: \[String\] = (\[.*\])`).FindSubmatch(source)
	if len(match) != 2 {
		t.Fatal("Swift vocabulary missing")
	}
	var swift []string
	if err := json.Unmarshal(match[1], &swift); err != nil {
		t.Fatal(err)
	}
	if len(swift) != len(emojis) {
		t.Fatal("Swift/Go vocabulary lengths differ")
	}
	for i, glyph := range emojis {
		if swift[i] != glyph {
			t.Fatalf("Swift/Go vocabulary differs at byte %d: %q / %q", i, swift[i], glyph)
		}
	}
}
