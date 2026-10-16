package eggclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
)

func TestSpawnEggRejectsOversizedInitialCodexPrompt(t *testing.T) {
	for _, prompt := range []string{strings.Repeat("x", 140*1024), strings.Repeat("Ω", egg.MaxInitialCodexPromptBytes/2+1), strings.Repeat("\x01", egg.MaxInitialCodexPromptBytes/6+1)} {
		cfg := &config.Config{Dir: t.TempDir()}
		for range 2 {
			client, err := SpawnEgg(cfg, "fixture", "codex", egg.UnsandboxedEggConfig(), 24, 80, cfg.Dir, false, false, false, EggIdentity{}, 0, SpawnEggOpts{InitialRun: &egg.RunTurnRequest{RunID: "fixture", Prompt: prompt}})
			var sizeErr *egg.InitialCodexPromptTooLargeError
			if client != nil || !errors.As(err, &sizeErr) || sizeErr.SizeBytes != egg.InitialCodexPromptArgBytes(prompt) || sizeErr.LimitBytes != egg.MaxInitialCodexPromptBytes {
				t.Fatalf("direct spawn: %v", err)
			}
		}
		if _, err := os.Stat(filepath.Join(cfg.Dir, "eggs")); !os.IsNotExist(err) {
			t.Fatalf("rejected spawn created egg state: %v", err)
		}
	}
}
