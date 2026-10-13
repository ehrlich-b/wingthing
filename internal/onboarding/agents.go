// Package onboarding describes local, user-selected provider setup.
package onboarding

import "os/exec"

type Agent struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Source    string   `json:"source"`
	Installer string   `json:"installer"`
	Login     []string `json:"login"`
}

// Discover only searches PATH. It never executes a binary or accesses a network.
func Discover() []Agent { return Detect(exec.LookPath) }
func Detect(lookup func(string) (string, error)) []Agent {
	agents := []Agent{
		{ID: "claude", Name: "Claude Code", Source: "https://code.claude.com/docs/en/setup", Installer: "curl -fsSL https://claude.ai/install.sh | bash", Login: []string{"auth", "login"}},
		{ID: "codex", Name: "Codex", Source: "https://github.com/openai/codex#quickstart", Installer: "curl -fsSL https://chatgpt.com/codex/install.sh | sh", Login: []string{"login"}},
	}
	for i := range agents {
		agents[i].Path, _ = lookup(agents[i].ID)
	}
	return agents
}
