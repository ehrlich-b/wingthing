package cmdutil

import (
	"strings"
	"testing"
)

func TestDecodeCLIAPIResponseIsBounded(t *testing.T) {
	var decoded struct {
		Value string `json:"value"`
	}
	if err := DecodeCLIAPIResponse(strings.NewReader(`{"value":"ok"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Value != "ok" {
		t.Fatalf("decoded value = %q", decoded.Value)
	}
	oversized := `{"padding":"` + strings.Repeat("x", maxCLIAPIResponseBytes) + `"}`
	if err := DecodeCLIAPIResponse(strings.NewReader(oversized), &decoded); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response error = %v", err)
	}
}
