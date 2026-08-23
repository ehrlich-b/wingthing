package main

import (
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestFinderRelayURL(t *testing.T) {
	cfg := &config.Config{RoostURL: "https://configured.example/"}
	if got := finderRelayURL(cfg, ""); got != "https://configured.example" {
		t.Fatalf("configured relay = %q", got)
	}
	if got := finderRelayURL(cfg, "wss://other.example/ws"); got != "https://other.example/ws" {
		t.Fatalf("override relay = %q", got)
	}
}
