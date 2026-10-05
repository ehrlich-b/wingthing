package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedEggDiagnosticBoundAndRetention(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "egg.log"), []byte(strings.Repeat("startup\n", 30000)+"actual last startup failure"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"egg.owner", "session.principal", "egg.meta"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := preserveEggFailure(dir, errors.New("loopback bind denied"))
	if err != nil {
		t.Fatal(err)
	}
	cleanEggDir(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > failedEggLogBytes+4200 || !strings.Contains(string(data), "actual last startup failure") || !strings.Contains(string(data), "loopback bind denied") {
		t.Fatalf("diagnostic bytes=%d", len(data))
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("diagnostic mode %v %v", info, err)
	}
	for _, name := range []string{"egg.owner", "session.principal", "egg.meta"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("lost identity %s: %v", name, err)
		}
	}
}

func TestPromptReservationsSurviveWingReaping(t *testing.T) {
	for _, name := range []string{"prompt.lock", "prompt.fixture.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, artifact := range []string{name, "egg.owner", "session.principal", "egg.meta"} {
				if err := os.WriteFile(filepath.Join(dir, artifact), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cleanEggDir(dir)
			for _, artifact := range []string{name, "egg.owner", "session.principal", "egg.meta"} {
				if _, err := os.Stat(filepath.Join(dir, artifact)); err != nil {
					t.Fatalf("lost %s: %v", artifact, err)
				}
			}
		})
	}
}
