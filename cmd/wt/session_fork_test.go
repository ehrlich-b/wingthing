package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
)

func TestSessionForkCommandExposure(t *testing.T) {
	cmd, _, err := sessionCmd().Find([]string{"fork"})
	if err != nil || cmd.Name() != "fork" || cmd.Flags().Lookup("name") == nil || cmd.Flags().Lookup("json") == nil {
		t.Fatalf("fork CLI missing: %v", err)
	}
	if err := cmd.Args(cmd, nil); err == nil {
		t.Fatal("accepted missing source")
	}
	if err := cmd.Args(cmd, []string{"source", "extra"}); err == nil {
		t.Fatal("accepted extra arguments")
	}
}

func TestSessionForkCLIReportsUnsupportedProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WINGTHING_DIR", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	writeResumeSessionFixture(t, cfg, "source", "", "codex", t.TempDir(), "provider", "{}\n")
	cmd := sessionForkCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("name", "branch"); err != nil {
		t.Fatal(err)
	}
	err = cmd.RunE(cmd, []string{"source"})
	if err == nil || !strings.Contains(err.Error(), "only Claude") {
		t.Fatalf("fork refusal: %v", err)
	}
}
