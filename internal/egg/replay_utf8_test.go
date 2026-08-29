package egg

import (
	"testing"
	"unicode/utf8"
)

func TestFindSafeCutFallbackDoesNotSplitUTF8(t *testing.T) {
	data := []byte("a🙂tail")
	cut := findSafeCut(data, 3)
	if cut != 1 {
		t.Fatalf("UTF-8-safe cut = %d, want 1", cut)
	}
	if !utf8.Valid(data[:cut]) || !utf8.Valid(data[cut:]) {
		t.Fatal("safe replay-buffer cut split a UTF-8 sequence")
	}
}

func TestUTF8SafeCutBoundsInvalidContinuationRuns(t *testing.T) {
	data := []byte{'a', 0x80, 0x80, 0x80, 0x80, 'b'}
	if cut := utf8SafeCut(data, 4); cut != 4 {
		t.Fatalf("invalid-byte cut moved from 4 to %d", cut)
	}
}
