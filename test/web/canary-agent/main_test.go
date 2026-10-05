package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCanaryModelPolicy(t *testing.T) {
	const policy = `{"env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`
	const withHooks = `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"true"}]}]},"env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"},"disableAllHooks":false}`
	for _, tc := range []struct {
		name, settings, model string
		wantOK                bool
	}{
		{"deployment policy", policy, "claude-sonnet-5", true},
		{"lifecycle hooks", withHooks, "claude-sonnet-5", true},
		{"formatted JSON", "{\n  \"env\": {\"CLAUDE_CODE_EFFORT_LEVEL\": \"xhigh\"}\n}", "claude-sonnet-5", true},
		{"wrong model", policy, "opus", false},
		{"missing model", policy, "", false},
		{"wrong effort", `{"env":{"CLAUDE_CODE_EFFORT_LEVEL":"max"}}`, "claude-sonnet-5", false},
		{"missing effort", `{"hooks":{}}`, "claude-sonnet-5", false},
		{"non-string effort", `{"env":{"CLAUDE_CODE_EFFORT_LEVEL":true}}`, "claude-sonnet-5", false},
		{"invalid JSON", `{"env":`, "claude-sonnet-5", false},
		{"null settings", `null`, "claude-sonnet-5", false},
	} {
		for _, file := range []bool{false, true} {
			name := tc.name + "/inline"
			if file {
				name = tc.name + "/file"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				settings := tc.settings
				if file {
					settings = filepath.Join(t.TempDir(), "lifecycle settings.json")
					if err := os.WriteFile(settings, []byte(tc.settings), 0600); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"--settings", settings}
				if tc.model != "" {
					args = append(args, "--model", tc.model)
				}
				want := "CANARY_MODEL_POLICY ok=false saved_model= theme=\r\n"
				if tc.wantOK {
					want = "CANARY_MODEL_POLICY ok=true saved_model= theme=\r\n"
				}
				if got := canaryModelPolicyOutput(t, args); got != want {
					t.Fatalf("model policy = %q, want %q", got, want)
				}
			})
		}
	}
	for _, args := range [][]string{
		{"--model", "claude-sonnet-5"},
		{"--model", "claude-sonnet-5", "--settings"},
		{"--model", "claude-sonnet-5", "--settings", filepath.Join(t.TempDir(), "missing.json")},
	} {
		t.Run("unavailable settings", func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if got := canaryModelPolicyOutput(t, args); got != "CANARY_MODEL_POLICY ok=false saved_model= theme=\r\n" {
				t.Fatalf("unavailable policy = %q", got)
			}
		})
	}
}

func TestCanaryModelPolicyPreservesPreferences(t *testing.T) {
	const policy = `{"hooks":{},"env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`
	for _, tc := range []struct {
		name, prefs, want string
	}{
		{"alice initial", `{"model":"opus","theme":"alice-theme","env":{"PERSONAL":"alice-only","CLAUDE_CODE_EFFORT_LEVEL":"max"}}`, "saved_model=opus theme=alice-theme"},
		{"alice updated", `{"model":"opus","theme":"alice-edited","env":{"PERSONAL":"alice-only","CLAUDE_CODE_EFFORT_LEVEL":"max"}}`, "saved_model=opus theme=alice-edited"},
		{"bob fresh", "", "saved_model= theme="},
	} {
		for _, file := range []bool{false, true} {
			name := tc.name + "/inline"
			if file {
				name = tc.name + "/file"
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				t.Setenv("HOME", home)
				prefsPath := filepath.Join(home, ".claude", "settings.json")
				if tc.prefs != "" {
					if err := os.MkdirAll(filepath.Dir(prefsPath), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(prefsPath, []byte(tc.prefs), 0600); err != nil {
						t.Fatal(err)
					}
				}
				settings := policy
				if file {
					settings = filepath.Join(t.TempDir(), "policy.json")
					if err := os.WriteFile(settings, []byte(policy), 0600); err != nil {
						t.Fatal(err)
					}
				}
				want := "CANARY_MODEL_POLICY ok=true " + tc.want + "\r\n"
				if got := canaryModelPolicyOutput(t, []string{"--model", "claude-sonnet-5", "--settings", settings}); got != want {
					t.Fatalf("model policy = %q, want %q", got, want)
				}
				data, err := os.ReadFile(prefsPath)
				if tc.prefs == "" {
					if !os.IsNotExist(err) {
						t.Fatalf("fresh user preferences were created: %s, %v", data, err)
					}
				} else if err != nil || !bytes.Equal(data, []byte(tc.prefs)) {
					t.Fatalf("personal preferences changed: %s, %v", data, err)
				}
			})
		}
	}
}

func canaryModelPolicyOutput(t *testing.T, args []string) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	oldArgs, oldStdout := os.Args, os.Stdout
	defer func() { os.Args, os.Stdout = oldArgs, oldStdout }()
	os.Args = append([]string{"claude"}, args...)
	os.Stdout = writer
	printModelPolicy()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
