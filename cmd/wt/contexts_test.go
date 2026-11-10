package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/config"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func TestContextCLIHistoryJSONAndRemoteAdd(t *testing.T) {
	for _, context := range []string{"home", "work", ""} {
		t.Run("context="+context, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("WINGTHING_DIR", state)
			if err := config.SaveWingConfig(state, &config.WingConfig{MachineContext: context}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				dir := filepath.Join(state, "eggs", fmt.Sprintf("id-%d", i))
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for name, value := range map[string]string{"chat.jsonl.gz": "fixture", "egg.meta": "agent=codex\ncwd=/projects/product\n"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			var out bytes.Buffer
			streams := remotepkg.IO{Out: &out, ErrOut: io.Discard}
			if err := executeCLI(t.Context(), []string{"session", "list", "--json"}, streams); err != nil {
				t.Fatal(err)
			}
			var rows []map[string]any
			if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("rows=%s", out.Bytes())
			}
			for _, row := range rows {
				if got, ok := row["context"]; !ok || got != context {
					t.Fatalf("context missing or wrong: %s", out.Bytes())
				}
			}
			if root := os.Getenv("WT_CONTEXT_EVIDENCE"); root != "" {
				name := context
				if name == "" {
					name = "unset"
				}
				if err := os.WriteFile(filepath.Join(root, "session-list-"+name+".json"), out.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := executeCLI(t.Context(), []string{"remote", "add", "studio", "fake-host", "--context", "work"}, streams); err != nil {
				t.Fatal(err)
			}
			remotes, err := config.LoadRemotes(state)
			if err != nil || remotes["studio"].Context != "work" {
				t.Fatalf("context not persisted: %+v %v", remotes, err)
			}
			if err := executeCLI(t.Context(), []string{"remote", "add", "other", "fake-host"}, streams); err != nil {
				t.Fatal(err)
			}
			ssh := filepath.Join(state, "ssh")
			inventory := `{"version":"fixture","contract_version":"v1","sessions":[{"id":"remote-full-id","pid":42}]}`
			if err := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf '%s\\n' '"+inventory+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			streams.SSHPath = ssh
			out.Reset()
			if err := executeCLI(t.Context(), []string{"session", "ps", "--json"}, streams); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("active remote rows = %s", out.Bytes())
			}
			expected := map[string]string{"studio": "work", "other": ""}
			for _, row := range rows {
				machine, ok := row["machine"].(string)
				if !ok {
					t.Fatal("missing machine")
				}
				want, ok := expected[machine]
				if !ok || row["context"] != want {
					t.Fatalf("wrong remote context: %s", out.Bytes())
				}
			}
			if root := os.Getenv("WT_CONTEXT_EVIDENCE"); root != "" {
				if err := os.WriteFile(filepath.Join(root, "session-ps-contexts.json"), out.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}

		})
	}
}
