package main

import (
	"io"
	"strings"
	"testing"
)

func closeForTest(t *testing.T, name string, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close %s: %v", name, err)
	}
}

func TestDecodeCLIAPIResponseIsBounded(t *testing.T) {
	var decoded struct {
		Value string `json:"value"`
	}
	if err := decodeCLIAPIResponse(strings.NewReader(`{"value":"ok"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Value != "ok" {
		t.Fatalf("decoded value = %q", decoded.Value)
	}
	oversized := `{"padding":"` + strings.Repeat("x", maxCLIAPIResponseBytes) + `"}`
	if err := decodeCLIAPIResponse(strings.NewReader(oversized), &decoded); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response error = %v", err)
	}
}
