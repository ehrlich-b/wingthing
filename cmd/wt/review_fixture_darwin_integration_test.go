//go:build integration && darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "codex" {
		os.Exit(runReviewFixtureAgent(os.Args[1:]))
	}
	os.Exit(m.Run())
}
