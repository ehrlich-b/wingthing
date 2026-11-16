package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/phonepair"
	"golang.org/x/crypto/hkdf"
)

func emojiTerminal(t *testing.T) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "en_US.UTF-8")
}

func TestPairEmojiTerminalLocales(t *testing.T) {
	for _, tc := range []struct {
		name, term, all, ctype, lang string
		want                         bool
	}{
		{"UTF-8", "xterm", "", "", "en_US.UTF-8", true},
		{"utf8", "screen", "C.utf8", "", "C", true},
		{"ctype wins", "xterm", "", "en_US.UTF-8", "C", true},
		{"all wins", "xterm", "C", "en_US.UTF-8", "en_US.UTF-8", false},
		{"ctype C", "xterm", "", "C", "en_US.UTF-8", false},
		{"legacy", "xterm", "", "", "en_US.ISO8859-1", false},
		{"dumb", "dumb", "en_US.UTF-8", "", "", false},
		{"unset", "", "", "", "", false},
		{"modifier", "xterm", "de_DE.UTF-8@euro", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", tc.term)
			t.Setenv("LC_ALL", tc.all)
			t.Setenv("LC_CTYPE", tc.ctype)
			t.Setenv("LANG", tc.lang)
			if got := pairTerminalEmoji(); got != tc.want {
				t.Fatalf("emoji=%v, want %v", got, tc.want)
			}
			var out bytes.Buffer
			showPairConfirmation(&out, phonepair.PairStatus{Emoji: "😀 😃 😄 😁"})
			if strings.Contains(out.String(), "1. 😀 grinning face\n") != tc.want {
				t.Fatal(out.String())
			}
			if strings.Contains(out.String(), "Emoji unavailable in this terminal; compare names in order.") == tc.want {
				t.Fatal(out.String())
			}
			if !tc.want && (!strings.Contains(out.String(), "1. grinning face\n") || strings.Contains(out.String(), "😀")) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestPairConfirmationCopyUnchanged(t *testing.T) {
	emojiTerminal(t)
	for _, scope := range []string{phonepair.ScopeControl, phonepair.ScopeInventory} {
		var out bytes.Buffer
		showPairConfirmation(&out, phonepair.PairStatus{Emoji: "😀 😃 😄 😁", Fingerprint: "0123456789abcdef", Name: "My iPhone", Scope: scope})
		access := "Control sessions on this machine"
		if scope == phonepair.ScopeInventory {
			access = "Inventory only: list sessions and machines on this machine"
		}
		want := "\nComparison labels:\n1. 😀 grinning face\n2. 😃 smiling face with open mouth\n3. 😄 smiling face with open mouth and smiling eyes\n4. 😁 grinning face with smiling eyes\nFingerprint: 0123 4567 89ab cdef\nAccess: " + access + "\nMy iPhone wants to pair. Enter to allow.\nOnly allow if the full fingerprint and all 4 labels match the device in order. Ctrl-C cancels.\n"
		if out.String() != want {
			t.Fatalf("confirmation changed:\n%s", out.String())
		}
		t.Log(out.String())
	}
}

func TestPairEmojiMatchesSwiftSharedSecretVectors(t *testing.T) {
	emojiTerminal(t)
	data, err := os.ReadFile("../../internal/phonepair/testdata/emoji_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Invitation phonepair.Invitation `json:"invitation"`
		Alice, Bob string
		Vectors    []struct {
			Name       string
			Shared     string `json:"shared_hex"`
			Key        string `json:"key_hex"`
			Emoji      string
			MultiEmoji string `json:"multi_emoji"`
		}
		Multi []string `json:"multi_codepoint"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("../../mobile/ios/WingthingCore/Sources/WingthingCore/PairingEmoji.swift")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`let pairingEmoji: \[String\] = (\[.*\])`).FindSubmatch(source)
	var vocabulary []string
	if len(match) != 2 {
		t.Fatal("Swift vocabulary missing")
	}
	if err := json.Unmarshal(match[1], &vocabulary); err != nil {
		t.Fatal(err)
	}
	if len(vocabulary) != 256 {
		t.Fatalf("Swift vocabulary has %d entries", len(vocabulary))
	}
	// Bind the portable oracle to the actual Swift expressions, so a changed
	// transcript, HKDF domain, HMAC or byte ordering requires updating this proof.
	swift, err := os.ReadFile("../../mobile/ios/WingthingCore/Sources/WingthingCore/PhonePairing.swift")
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{
		`let transcript = "wt-pair-v1\n\(invitation.wingID)\n\(invitation.key)\n\(invitation.id)\n\(message)\n\(peer)"`,
		`HKDF<SHA256>.deriveKey(inputKeyMaterial: SymmetricKey(data: output.prefix(size)), salt: Data(SHA256.hash(data: Data(transcript.utf8))), info: Data("wt-pair-v1".utf8), outputByteCount: 32)`,
		`Array(HMAC<SHA256>.authenticationCode(for: Data("wt-emoji-v1".utf8), using: key)).prefix(4).map { pairingEmoji[Int($0)] }.joined(separator: " ")`,
	} {
		if !strings.Contains(string(swift), expression) {
			t.Fatalf("Swift derivation changed: %s", expression)
		}
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			shared, err := hex.DecodeString(vector.Shared)
			if err != nil {
				t.Fatal(err)
			}
			q := fixture.Invitation
			transcript := "wt-pair-v1\n" + q.WingID + "\n" + q.Key + "\n" + q.ID + "\n" + fixture.Alice + "\n" + fixture.Bob
			salt := sha256.Sum256([]byte(transcript))
			key := make([]byte, 32)
			if _, err := io.ReadFull(hkdf.New(sha256.New, shared, salt[:], []byte("wt-pair-v1")), key); err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(key) != vector.Key {
				t.Fatal("HKDF fixture mismatch")
			}
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte("wt-emoji-v1"))
			var glyphs []string
			for _, index := range mac.Sum(nil)[:4] {
				glyphs = append(glyphs, vocabulary[int(index)])
			}
			expected := strings.Join(glyphs, " ")
			if expected != vector.Emoji || phonepair.CheckEmoji(key) != expected || phonepair.EmojiFromShared(q, shared, fixture.Alice, fixture.Bob) != expected {
				t.Fatalf("Go/Swift/fixture disagreement: %q", expected)
			}
			assertPairGlyphRows(t, expected)
			assertPairGlyphRows(t, vector.MultiEmoji)
			t.Logf("shared-secret vector %s: %s", vector.Name, expected)
		})
	}
	// The production vocabulary currently contains only single codepoints.
	// Synthetic ZWJ, variation-selector and skin-tone entries exercise the same
	// whitespace-delimited display path without changing the wire vocabulary.
	assertPairGlyphRows(t, strings.Join(fixture.Multi, " "))
}

func assertPairGlyphRows(t *testing.T, emoji string) {
	t.Helper()
	var out bytes.Buffer
	showPairConfirmation(&out, phonepair.PairStatus{Emoji: emoji})
	var got []string
	for _, line := range strings.Split(out.String(), "\n") {
		if len(line) > 3 && line[1:3] == ". " {
			got = append(got, strings.Fields(line)[1])
		}
	}
	if len(got) != 4 || strings.Join(got, " ") != emoji {
		t.Fatalf("glyph sequence changed: %s", out.String())
	}
	for i, glyph := range strings.Fields(emoji) {
		if !strings.Contains(out.String(), fmt.Sprintf("%d. %s %s\n", i+1, glyph, phonepair.EmojiLabels(glyph))) {
			t.Fatal(out.String())
		}
	}
}
