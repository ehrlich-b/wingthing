package dashboard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

// Detection only inspects PATH. Opening the navigator never runs a provider,
// installer, version check or authentication process.
func (s *state) rescanProviders() {
	s.providerPaths = make(map[string]string)
	for _, name := range []string{"claude", "codex"} {
		if binary, err := exec.LookPath(name); err == nil {
			s.providerPaths[name] = binary
		}
	}
}

func (s *state) firstRun() bool {
	if !s.onboarding || s.machineFilter != "" && s.machineFilter != "local" {
		return false
	}
	for _, m := range s.machines {
		if len(m.sessions) > 0 {
			return false
		}
	}
	return true
}

func (s *state) firstRunLines() []string {
	lines := []string{"", "Local only: this machine + phones paired on your network",
		"A wing is the daemon hosting sessions; a roost is the machine you work on.",
		"Free local / LAN / tailnet / saved SSH; your provider may need its own account and network."}
	if len(s.providerPaths) == 0 {
		return append(lines, "No supported agent CLIs on PATH.", "n: Choose Claude Code or Codex   s: Skip; open sessions / shell")
	}
	for _, name := range []string{"claude", "codex"} {
		if binary := s.providerPaths[name]; binary != "" {
			lines = append(lines, "Found on this machine: "+name+" · "+binary)
		} else {
			lines = append(lines, name+": not installed")
		}
	}
	return append(lines, "n: Use detected agent / Add more agents   s: Skip; open sessions / shell")
}

func (s *state) beginOnboarding() {
	s.rescanProviders()
	if s.agent != "claude" && s.agent != "codex" {
		s.agent = "claude"
		if s.providerPaths["claude"] == "" && s.providerPaths["codex"] != "" {
			s.agent = "codex"
		}
	}
	s.selectProvider()
	if len(s.providerPaths) == 0 {
		s.mode = agentChooser
	}
}

func (s *state) selectProvider() {
	s.message = ""
	s.mode = installerConsent
	if s.providerPaths[s.agent] != "" {
		s.mode = providerVerify
	}
}

func (s *state) reviewCWD() {
	s.mode, s.input = newCWD, s.defaultCWD
	if s.target.CWD != "" {
		s.input = s.target.CWD
	}
}

func (s *state) handleOnboarding(key string) (*action, bool) {
	switch key {
	case "escape", "b":
		s.mode, s.message = navigate, ""
		return nil, false
	case "s":
		s.agent = ""
		s.reviewCWD()
		return nil, false
	case "c":
		s.agent = "claude"
		s.selectProvider()
		return nil, false
	case "o":
		s.agent = "codex"
		s.selectProvider()
		return nil, false
	case "a":
		s.mode = agentChooser
		return nil, false
	case "r":
		s.rescanProviders()
		s.selectProvider()
		return nil, false
	case "enter":
		switch s.mode {
		case agentChooser:
			s.selectProvider()
		case onboardingRecovery:
			// Always return to review before retrying an installer or login.
			s.mode, s.message = s.onboardingStage, ""
		case installerConsent, providerVerify, providerLogin:
			return &action{kind: s.mode}, false
		}
	}
	return nil, false
}

func installerCommand(name string) string {
	if name == "codex" {
		return "curl -fsSL https://chatgpt.com/codex/install.sh | sh"
	}
	return "curl -fsSL https://claude.ai/install.sh | bash"
}

func (s *state) onboardingLines() []string {
	host, _ := os.Hostname()
	lines := []string{"Local only: " + host + " + phones paired on your network", "Selected agent: " + s.agent}
	switch s.mode {
	case agentChooser:
		lines = append(lines, "Choose Claude Code or Codex", "c: Claude Code   o: Codex   Enter: selected agent", "s: Skip; open sessions / shell")
	case installerConsent:
		lines = append(lines, "Install on local ("+host+") as your current user:", installerCommand(s.agent),
			"Download the selected vendor's native installer and executable; network required.",
			"Enter = consent and install · Esc = cancel", "Retry with fresh consent: return here before running again.",
			"r: Rescan PATH   b: Back   c/o: choose another agent")
	case providerVerify:
		lines = append(lines, "Verify selected provider: "+s.agent+" --version", "Executable: "+s.providerPaths[s.agent],
			"Continue to the selected agent's own login after verification.", "A version check is not authentication or a ready-session receipt.",
			"Enter: verify   a: Add more agents   c/o: choose another   r: Rescan PATH   b: Back")
	case providerLogin:
		lines = append(lines, "Continue to the selected agent's own login", "Provider authentication uses the native terminal; provider billing may apply.",
			"Enter: open native login   c/o: choose another   b: Back")
	case onboardingRecovery:
		lines = append(lines, s.message, "Enter: Retry with fresh consent   r: Rescan PATH   c/o: Choose another   b: Back",
			"No automatic retry. Existing inventory remains available.")
	}
	return lines
}

// The navigator releases its terminal before invoking a vendor. Streams go
// straight to that terminal and are never stored in diagnostics or telemetry.
func (s *state) runOnboarding(ctx context.Context, stage mode, streams remotepkg.IO) error {
	s.onboardingStage = stage
	fail := func(message string) error {
		s.mode, s.message = onboardingRecovery, message
		return nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fail("The selected native installer supports macOS and Linux only.")
	}
	var command *exec.Cmd
	if stage == installerConsent {
		// pipefail preserves a failed download instead of reporting the empty
		// shell pipeline as a successful installation.
		command = exec.CommandContext(ctx, "bash", "-o", "pipefail", "-c", installerCommand(s.agent))
	} else {
		s.rescanProviders()
		binary := s.providerPaths[s.agent]
		if binary == "" {
			return fail("PATH stage failed: selected provider absent. Add " + filepath.Join(os.Getenv("HOME"), ".local", "bin") + " to PATH; Rescan PATH after fixing it.")
		}
		args := []string{"--version"}
		if stage == providerLogin {
			args = []string{"login"}
			if s.agent == "claude" {
				args = []string{"auth", "login"}
			}
		}
		command = exec.CommandContext(ctx, binary, args...)
	}
	command.Stdin, command.Stdout, command.Stderr = streams.In, streams.Out, streams.ErrOut
	if err := command.Run(); err != nil {
		return fail(fmt.Sprintf("%s stage failed or cancelled. Review the native terminal's error and permissions; no automatic sudo.", map[mode]string{installerConsent: "Install", providerVerify: "Verification", providerLogin: "Provider login"}[stage]))
	}
	s.message = ""
	switch stage {
	case installerConsent:
		s.rescanProviders()
		if s.providerPaths[s.agent] == "" {
			return fail("PATH stage: installer finished but selected binary is absent on PATH. Check " + filepath.Join(os.Getenv("HOME"), ".local", "bin") + "; Rescan PATH after fixing it.")
		}
		s.mode = providerVerify
	case providerVerify:
		s.mode = providerLogin
	case providerLogin:
		s.reviewCWD()
	}
	return nil
}
