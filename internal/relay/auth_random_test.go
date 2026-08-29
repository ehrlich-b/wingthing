package relay

import (
	"bytes"
	"errors"
	"testing"
)

func TestGenerateTokenFromUsesAllRandomBytes(t *testing.T) {
	token, err := generateTokenFrom(bytes.NewReader(bytes.Repeat([]byte{0xab}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if want := string(bytes.Repeat([]byte("ab"), 32)); token != want {
		t.Fatalf("token = %q, want %q", token, want)
	}
}

func TestGenerateTokenFromFailsOnRandomnessError(t *testing.T) {
	if token, err := generateTokenFrom(errorReader{}); err == nil || token != "" {
		t.Fatalf("token = %q, err = %v; want empty token and error", token, err)
	}
}

func TestGenerateUserCodeFromUsesSecureAlphabet(t *testing.T) {
	code, err := generateUserCodeFrom(bytes.NewReader(bytes.Repeat([]byte{0}, 6)), 6)
	if err != nil {
		t.Fatal(err)
	}
	if code != "AAAAAA" {
		t.Fatalf("device code = %q", code)
	}
}

func TestGenerateUserCodeFromFailsClosed(t *testing.T) {
	if code, err := generateUserCodeFrom(errorReader{}, 6); err == nil || code != "" {
		t.Fatalf("device code = %q, err = %v; want empty code and error", code, err)
	}
	if _, err := generateUserCodeFrom(bytes.NewReader(nil), 0); err == nil {
		t.Fatal("zero-length device code was accepted")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("random source unavailable") }
