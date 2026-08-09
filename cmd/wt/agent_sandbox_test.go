package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func TestDirectAgentSandboxConfigAppliesOpenCodeProfile(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	home := t.TempDir()
	cfg := directAgentSandboxConfig("opencode", "standard", home, []string{"/work/project"})

	if cfg.NetworkNeed != sandbox.NetworkHTTPS {
		t.Fatalf("NetworkNeed = %v, want https", cfg.NetworkNeed)
	}
	if cfg.UserHome != home {
		t.Fatalf("UserHome = %q, want %q", cfg.UserHome, home)
	}
	for _, want := range []string{
		"/work/project",
		filepath.Join(home, ".config/opencode"),
		filepath.Join(home, ".local/share/opencode"),
		filepath.Join(home, ".local/state/opencode"),
		filepath.Join(home, ".cache/opencode"),
	} {
		if !hasSandboxMount(cfg.Mounts, want) {
			t.Errorf("missing writable mount %q in %#v", want, cfg.Mounts)
		}
	}
}

func TestDirectAgentSandboxConfigCapabilities(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	tests := []struct {
		name      string
		agent     string
		isolation string
		want      sandbox.NetworkNeed
	}{
		{name: "unknown stays offline", agent: "custom", isolation: "standard", want: sandbox.NetworkNone},
		{name: "ollama gets localhost", agent: "ollama", isolation: "strict", want: sandbox.NetworkLocal},
		{name: "agent profile drills https", agent: "gemini", isolation: "standard", want: sandbox.NetworkHTTPS},
		{name: "explicit network remains full", agent: "gemini", isolation: "network", want: sandbox.NetworkFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := directAgentSandboxConfig(tt.agent, tt.isolation, t.TempDir(), nil)
			if cfg.NetworkNeed != tt.want {
				t.Fatalf("NetworkNeed = %v, want %v", cfg.NetworkNeed, tt.want)
			}
		})
	}
}

func TestDirectAgentSandboxConfigUsesExplicitLocalProvider(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "http://127.0.0.1:4000/v1")
	cfg := directAgentSandboxConfig("codex", "standard", t.TempDir(), nil)
	if cfg.NetworkNeed != sandbox.NetworkLocal {
		t.Fatalf("NetworkNeed = %v, want local", cfg.NetworkNeed)
	}
	if len(cfg.Domains) != 1 || cfg.Domains[0] != "127.0.0.1" {
		t.Fatalf("Domains = %q, want loopback provider only", cfg.Domains)
	}
}

func TestDirectAgentEnvPreservesHomeAndAddsProxy(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "")
	home := t.TempDir()
	env := directAgentEnv("opencode", home, 43210)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{
		"\nHOME=" + home + "\n",
		"\nHTTPS_PROXY=http://localhost:43210\n",
		"\nHTTP_PROXY=http://localhost:43210\n",
		"\nNODE_USE_ENV_PROXY=1\n",
		"\nGIT_TERMINAL_PROMPT=0\n",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment missing %q", strings.TrimSpace(want))
		}
	}
	if !strings.Contains(joined, filepath.Join(home, ".local", "bin")) {
		t.Error("PATH does not include the user's local bin directory")
	}
}

func TestDirectAgentEnvBypassesProxyForExplicitLocalProvider(t *testing.T) {
	t.Setenv("WT_PROVIDER_BASE_URL", "http://localhost:4000/v1")
	t.Setenv("NO_PROXY", "example.test")
	t.Setenv("no_proxy", "")
	env := directAgentEnv("codex", t.TempDir(), 0)
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, want := range []string{"\nNO_PROXY=example.test,localhost\n", "\nno_proxy=localhost\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment missing %q", strings.TrimSpace(want))
		}
	}
}

func hasSandboxMount(mounts []sandbox.Mount, source string) bool {
	for _, mount := range mounts {
		if mount.Source == source && !mount.ReadOnly {
			return true
		}
	}
	return false
}
