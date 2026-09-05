package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/agent"
)

func TestIsolatedClaudePolicyProjectsOnlyModelAndEffort(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
		invalid            bool
	}{
		{"current deployment", `{"model":"claude-sonnet-4-6","effortLevel":"high","env":{"CLAUDE_CODE_EFFORT_LEVEL":"max"}}`, `{"model":"claude-sonnet-4-6","effortLevel":"high","env":{"CLAUDE_CODE_EFFORT_LEVEL":"max"}}`, false},
		{"sonnet five", `{"model":"claude-sonnet-5","effortLevel":"xhigh","env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`, `{"model":"claude-sonnet-5","effortLevel":"xhigh","env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`, false},
		{"no policy", `{"theme":"host-theme","apiKey":"host-secret"}`, `{}`, false},
		{"model only", `{"model":"claude-sonnet-5"}`, `{"model":"claude-sonnet-5"}`, false},
		{"secret and preference boundary", `{"model":"claude-sonnet-5","apiKey":"host-secret","apiKeyHelper":"host-helper","hooks":{"SessionStart":["host-command"]},"permissions":{"allow":["Bash"]},"theme":"host-theme","env":{"ANTHROPIC_API_KEY":"host-secret","CLAUDE_CODE_EFFORT_LEVEL":"xhigh","UNRELATED":"host-value"}}`, `{"model":"claude-sonnet-5","env":{"CLAUDE_CODE_EFFORT_LEVEL":"xhigh"}}`, false},
		{"malformed", `{"apiKey":"host-secret",`, "", true},
		{"wrong type", `{"model":123}`, "", true},
		{"unsupported effort", `{"effortLevel":"unknown"}`, "", true},
		{"max is environment only", `{"effortLevel":"max"}`, "", true},
		{"unsupported environment effort", `{"env":{"CLAUDE_CODE_EFFORT_LEVEL":"unknown"}}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, ".claude", "settings.json")
			writePolicyFixture(t, path, tc.source)
			args, err := isolatedClaudePolicyArgs("claude", true)
			if tc.invalid {
				if err == nil || len(args) != 0 {
					t.Fatalf("invalid policy accepted: %q, %v", args, err)
				}
				if strings.Contains(err.Error(), "host-secret") {
					t.Fatal("error disclosed host settings")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]any{}
			for i := 0; i < len(args); i += 2 {
				if i+1 == len(args) {
					t.Fatalf("unpaired argv: %q", args)
				}
				switch args[i] {
				case "--model":
					got["model"] = args[i+1]
				case "--settings":
					if err := json.Unmarshal([]byte(args[i+1]), &got); err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatalf("unexpected argv: %q", args)
				}
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("policy = %#v, want %#v", got, want)
			}
			assertPolicyFixture(t, path, tc.source)
		})
	}
}

func TestIsolatedClaudePolicyReloadsWithoutTouchingUserProfiles(t *testing.T) {
	host := t.TempDir()
	t.Setenv("HOME", host)
	policyPath := filepath.Join(host, ".claude", "settings.json")
	users := []string{t.TempDir(), t.TempDir()}
	for _, user := range users {
		writePolicyFixture(t, filepath.Join(user, ".claude", "settings.json"), `{"model":"opus","theme":"dark","env":{"PERSONAL":"keep"}}`)
		writePolicyFixture(t, filepath.Join(user, ".claude", ".claude.json"), `{"hasCompletedOnboarding":true,"theme":"dark","projects":{"keep":true}}`)
		writePolicyFixture(t, filepath.Join(user, ".claude", ".credentials.json"), `{"fixture":"owner-only"}`)
	}
	for _, model := range []string{"claude-sonnet-4-6", "claude-sonnet-5"} {
		writePolicyFixture(t, policyPath, `{"model":"`+model+`"}`)
		for _, user := range users {
			if err := prepareIsolatedClaudeConfig(user, map[string]string{}); err != nil {
				t.Fatal(err)
			}
			args, err := isolatedClaudePolicyArgs("claude", true)
			if err != nil || len(args) != 2 || args[0] != "--model" || args[1] != model {
				t.Fatalf("new launch did not load current policy: %q, %v", args, err)
			}
			assertPolicyFixture(t, filepath.Join(user, ".claude", "settings.json"), `{"model":"opus","theme":"dark","env":{"PERSONAL":"keep"}}`)
			assertPolicyFixture(t, filepath.Join(user, ".claude", ".claude.json"), `{"hasCompletedOnboarding":true,"theme":"dark","projects":{"keep":true}}`)
			assertPolicyFixture(t, filepath.Join(user, ".claude", ".credentials.json"), `{"fixture":"owner-only"}`)
		}
	}
}

func TestIsolatedClaudePolicyKeepsExplicitSessionModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePolicyFixture(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"claude-sonnet-5"}`)
	policy, err := isolatedClaudePolicyArgs("claude", true)
	if err != nil {
		t.Fatal(err)
	}
	_, args, _ := agent.InteractiveInvocation("claude", true, "saved-session", append(policy, "--model", "opus")...)
	want := []string{"--dangerously-skip-permissions", "--resume", "saved-session", "--model", "claude-sonnet-5", "--model", "opus"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("argv = %q, want %q", args, want)
	}
}

func TestIsolatedClaudePolicyDoesNotReadHostForPersonalOrOtherAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writePolicyFixture(t, filepath.Join(home, ".claude", "settings.json"), "invalid")
	for _, tc := range []struct {
		agent    string
		isolated bool
	}{{"claude", false}, {"codex", true}, {"shell", true}} {
		args, err := isolatedClaudePolicyArgs(tc.agent, tc.isolated)
		if err != nil || len(args) != 0 {
			t.Fatalf("unrelated session inherited policy: %q, %v", args, err)
		}
	}
}

func TestIsolatedClaudePolicyMissingAndUnreadable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	args, err := isolatedClaudePolicyArgs("claude", true)
	if err != nil || len(args) != 0 {
		t.Fatalf("missing host policy = %q, %v", args, err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude", "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := isolatedClaudePolicyArgs("claude", true); err == nil {
		t.Fatal("unreadable policy silently fell back to vendor default")
	}
}

func TestExistingClaudeHelperDoesNotRewritePersonalSettings(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	helper, err := json.Marshal("cat " + filepath.Join(home, ".anthropic_key"))
	if err != nil {
		t.Fatal(err)
	}
	personal := "{\n  \"theme\": \"user-theme\", \"model\": \"opus\",\n  \"apiKeyHelper\": " + string(helper) + "\n}\n"
	writePolicyFixture(t, path, personal)
	for _, key := range []string{"fixture-key-before", "fixture-key-after"} {
		if err := setupAPIKeyHelper("claude", map[string]string{"ANTHROPIC_API_KEY": key}, home); err != nil {
			t.Fatal(err)
		}
		assertPolicyFixture(t, path, personal)
		assertPolicyFixture(t, filepath.Join(home, ".anthropic_key"), key)
	}
}

func writePolicyFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertPolicyFixture(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("profile changed at %s: %q, %v", path, data, err)
	}
}
