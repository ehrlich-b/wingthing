package main

import (
	"testing"
)

func TestSessionPromptCommandExposure(t *testing.T) {
	cmd := sessionCmd()
	found, _, err := cmd.Find([]string{"prompt"})
	if err != nil || found.Name() != "prompt" || found.Flags().Lookup("request-id") == nil || found.Flags().Lookup("json") == nil {
		t.Fatal("prompt CLI missing typed retry contract")
	}
}
