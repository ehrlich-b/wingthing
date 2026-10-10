package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionForkCommandExposure(t *testing.T) {
	cmd, _, err := sessionCmd().Find([]string{"fork"})
	if err != nil || cmd.Name() != "fork" || cmd.Flags().Lookup("name") == nil || cmd.Flags().Lookup("json") == nil || cmd.Flags().Lookup("client") == nil {
		t.Fatalf("fork CLI missing: %v", err)
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("accepted missing source")
	}
	if err := cmd.Args(cmd, []string{"source", "extra"}); err == nil {
		t.Fatal("accepted extra arguments")
	}
}

func TestSessionForkCLIUsesLocalWing(t *testing.T) {
	cfg := testLocalWing(t)
	if err := os.WriteFile(filepath.Join(cfg.Dir, "clients.yaml"), []byte("clients:\n  observer:\n    grants: [terminal.read]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := sessionForkCmd()
	cmd.SetContext(t.Context())
	if err := cmd.Flags().Set("client", "observer"); err != nil {
		t.Fatal(err)
	}
	err := cmd.RunE(cmd, []string{"source"})
	if err == nil || !strings.Contains(err.Error(), "terminal.start") {
		t.Fatalf("fork bypassed wing grant: %v", err)
	}
}
