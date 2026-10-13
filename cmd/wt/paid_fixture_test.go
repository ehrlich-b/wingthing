package main

import (
	"strings"
	"testing"
)

func TestPaidFixtureCLIRejectsNonLocalDevelopment(t *testing.T) {
	for _, args := range [][]string{
		{"--paid-fixture"},
		{"--paid-fixture", "--local"},
		{"--paid-fixture", "--dev"},
		{"--paid-fixture", "--local", "--dev", "--https"},
	} {
		cmd := serveCmd()
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "requires explicit --local --dev") {
			t.Fatalf("args %v: %v", args, err)
		}
	}
	cmd := serveCmd()
	if cmd.Flags().Lookup("paid-fixture").DefValue != "false" {
		t.Fatal("paid fixture must default OFF")
	}
}
