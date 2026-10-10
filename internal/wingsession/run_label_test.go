package wingsession

import (
	"strings"
	"testing"
)

func TestNormalizeRunLabel(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{"weekend canary", "weekend canary"},
		{"  Review Ω / tests: 🙂  ", "Review Ω / tests: 🙂"},
		{"one\n\t two\r three\x00\x1b\u202e", "one two three"},
		{"\t\x00", ""},
		{strings.Repeat("🙂", MaxRunLabelRunes), strings.Repeat("🙂", MaxRunLabelRunes)},
	} {
		got, err := NormalizeRunLabel(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("NormalizeRunLabel(%q) = %q, %v; want %q", test.raw, got, err, test.want)
		}
	}
	for _, raw := range []string{strings.Repeat("🙂", MaxRunLabelRunes+1), "bad\xff"} {
		if _, err := NormalizeRunLabel(raw); err == nil {
			t.Fatal("accepted oversized or invalid UTF-8 label")
		}
	}
}
