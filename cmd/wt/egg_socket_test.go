package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestSocketPathCLIFailsBeforeStateOrProcessCreation(t *testing.T) {
	old := config.ReleaseChannel
	t.Cleanup(func() { config.ReleaseChannel = old })
	t.Setenv("WINGTHING_PREVIEW_DIR", "")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	for _, channel := range []string{"stable", "preview"} {
		config.ReleaseChannel = channel
		for _, args := range [][]string{
			{"terminal", "--detach", "--json", "--", "/bin/sh"},
			{"egg", "codex", "--detach", "--json"},
			{"egg", "run", "--session-id", "socket-fixture", "--agent", "codex"},
		} {
			state := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
			t.Setenv("WINGTHING_DIR", state)
			err := executeCLI(context.Background(), args, remotepkg.IO{})
			assertSocketPathFailure(t, err)
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("%s %v wrote state before rejecting path: %v", channel, args, err)
			}
		}
	}
}

func TestSocketPathSpawnRejectsBeforeEggOrProcessCreation(t *testing.T) {
	state := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	// A nil policy would fail later; address rejection must precede all setup.
	_, err := eggclient.SpawnEgg(&config.Config{Dir: state}, "socket-fixture", "", nil, 24, 80, "", false, false, false, eggclient.EggIdentity{}, 0)
	assertSocketPathFailure(t, err)
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("spawn wrote state: %v", err)
	}
}

func assertSocketPathFailure(t *testing.T, err error) {
	t.Helper()
	var tooLong *egg.SocketPathTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("expected socket path preflight failure, got %v", err)
	}
	for _, fragment := range []string{fmt.Sprintf("%q", tooLong.Path), fmt.Sprintf("%d bytes", len(tooLong.Path)), "WINGTHING_DIR", "shorter"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error missing %q: %v", fragment, err)
		}
	}
}
