package sessionrole

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRolePromptAndHookTreatFileAsData(t *testing.T) {
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "roles"), 0700); err != nil {
		t.Fatal(err)
	}
	defaultPrompt, err := Prompt(state, "coordinator")
	if err != nil || !strings.Contains(defaultPrompt, "you are the coordinator node") || !strings.Contains(defaultPrompt, "roles/coordinator.md") {
		t.Fatalf("default: %q %v", defaultPrompt, err)
	}
	marker := filepath.Join(state, "must-not-exist")
	prompt := "You're the coordinator node.\nLiteral: $(touch " + marker + ") `touch " + marker + "` %s \\\n"
	if err := os.WriteFile(filepath.Join(state, "roles", "coordinator.md"), []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Prompt(state, "coordinator")
	if err != nil || loaded != prompt {
		t.Fatalf("file: %q %v", loaded, err)
	}
	hook := Hook(loaded)
	if hook["matcher"] != "startup|resume|clear|compact" {
		t.Fatal(hook)
	}
	command := hook["hooks"].([]any)[0].(map[string]any)["command"].(string)
	for _, source := range []string{"startup", "resume", "clear", "compact"} {
		cmd := exec.Command("/bin/sh", "-c", command)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","source":"` + source + `"}`)
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Output struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Output.Event != "SessionStart" || parsed.Output.Context != prompt {
			t.Fatalf("%s lost context: %s", source, output)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("prompt executed: %v", err)
	}
}

func TestRoleValidationAndBounds(t *testing.T) {
	state := t.TempDir()
	for _, role := range []string{"../secret", "coordinator.md", "-option", "UPPER", "a/b"} {
		if _, err := Prompt(state, role); err == nil {
			t.Errorf("accepted %q", role)
		}
	}
	if prompt, err := Prompt(state, ""); err != nil || prompt != "" {
		t.Fatalf("non-role: %q %v", prompt, err)
	}
	if _, err := Prompt(state, "scout"); err == nil {
		t.Fatal("missing custom role accepted")
	}
	if err := os.Mkdir(filepath.Join(state, "roles"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"", " \n", "nul\x00", strings.Repeat("a", MaxPromptBytes+1)} {
		if err := os.WriteFile(filepath.Join(state, "roles", "coordinator.md"), []byte(prompt), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Prompt(state, "coordinator"); err == nil {
			t.Fatal("invalid role accepted")
		}
	}
	if err := AddHook(map[string]any{"disableAllHooks": true}, "role"); err == nil {
		t.Fatal("disabled role hook accepted")
	}
}

func TestRoleRejectsProviderModesThatSkipHooks(t *testing.T) {
	for _, flag := range []string{"--bare", "--safe-mode", "--bare=true", "--safe-mode=true"} {
		if err := ValidateClaudeArgs([]string{"--model", "fixture", flag}); err == nil {
			t.Fatalf("accepted %s", flag)
		}
		if err := ValidateClaudeArgs([]string{"--", flag}); err != nil {
			t.Fatalf("positional text rejected: %v", err)
		}
	}
	if err := ValidateClaudeArgs([]string{"--restricted", "--setting-sources="}); err != nil {
		t.Fatal(err)
	}
}

func TestRoleSelectedEnvironmentRefusesSkippedHooksAndForeignConfig(t *testing.T) {
	for _, key := range []string{"CLAUDE_CODE_SIMPLE", "CLAUDE_CODE_SAFE_MODE"} {
		if err := ValidateClaudeEnvironment(map[string]string{key: "1"}); err == nil {
			t.Fatalf("accepted %s", key)
		}
		if err := AddHook(map[string]any{"env": map[string]any{key: "true"}}, "role"); err == nil {
			t.Fatalf("accepted settings env %s", key)
		}
		if err := ValidateClaudeEnvironment(map[string]string{key: "0"}); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), ".claude")
	if err := ValidateClaudeEnvironment(map[string]string{"CLAUDE_CONFIG_DIR": dir}, dir); err != nil {
		t.Fatal(err)
	}
	if err := ValidateClaudeEnvironment(map[string]string{"CLAUDE_CONFIG_DIR": t.TempDir()}, dir); err == nil {
		t.Fatal("unverified alternate role config accepted")
	}
}

func TestRoleFilesRefuseAliasesAndNonRegularData(t *testing.T) {
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "roles"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "roles", "coordinator.md")
	outside := filepath.Join(t.TempDir(), "fixture.md")
	if err := os.WriteFile(outside, []byte("unrelated fixture data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Prompt(state, "coordinator"); err == nil {
		t.Fatal("role followed a symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Prompt(state, "coordinator"); err == nil {
		t.Fatal("role read a multiply-linked file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Prompt(state, "coordinator"); err == nil {
		t.Fatal("role read a directory")
	}
}
