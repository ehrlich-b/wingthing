package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/egg"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

func TestRecoveryCLIListsEligibleSessions(t *testing.T) {
	state := t.TempDir()
	t.Setenv("WINGTHING_DIR", state)
	for _, id := range []string{"eligible", "stopped", "legacy"} {
		dir := filepath.Join(state, "eggs", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "egg.meta"), []byte("agent=claude\ncwd="+state+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if id == "legacy" {
			continue
		}
		if err := egg.WriteLaunchIntent(dir, egg.LaunchIntent{Version: 1, Agent: "claude", CWD: state, ProviderSessionID: "provider", Started: true}); err != nil {
			t.Fatal(err)
		}
		if id == "stopped" {
			if err := egg.MarkDeliberateStop(dir, "kill"); err != nil {
				t.Fatal(err)
			}
		}
	}
	cmd := sessionCmd()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"recover", "--list"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Sessions []eggclient.RecoverySession `json:"sessions"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].ID != "eligible" || !result.Sessions[0].Recoverable || result.Sessions[0].Status != "exited" {
		t.Fatalf("inventory: %+v", result)
	}
}
