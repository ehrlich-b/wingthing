package relay

import (
	"strings"
	"testing"
)

func TestDecodeInternalJSONResponseAcceptsBoundedResponse(t *testing.T) {
	var result struct {
		Found bool `json:"found"`
	}
	if err := decodeInternalJSONResponse(strings.NewReader(`{"found":true}`), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Found {
		t.Fatal("bounded response was not decoded")
	}
}

func TestDecodeInternalJSONResponseRejectsOversizedResponse(t *testing.T) {
	var result map[string]any
	response := `{"padding":"` + strings.Repeat("x", maxInternalJSONResponseBytes) + `"}`
	if err := decodeInternalJSONResponse(strings.NewReader(response), &result); err == nil {
		t.Fatal("oversized internal response was accepted")
	}
}
