package dashboard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/onboarding"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

func (s *state) isSetupMode() bool {
	switch s.mode {
	case agentSetup, installConsent, providerReady, setupFailure, setupInvoke:
		return true
	default:
		return false
	}
}

func (s *state) openSetup() {
	s.agents = onboarding.Discover()
	s.mode, s.setupError, s.setupCompleted = agentSetup, "", ""
}
func (s *state) setupHandle(key string) (*action, bool) {
	if key == "escape" || key == "q" {
		s.mode = navigate
		return nil, false
	}
	if s.mode == agentSetup {
		if key == "R" {
			s.openSetup()
		}
		if key == "1" || key == "2" {
			i := 0
			if key == "2" {
				i = 1
			}
			if i < len(s.agents) {
				s.setupAgent, s.agent = s.agents[i], s.agents[i].ID
				s.target = row{machine: "local", generation: s.machines["local"].generation}
				s.mode = providerReady
				if s.setupAgent.Path == "" {
					s.mode = installConsent
				}
			}
		}
		return nil, false
	}
	if key == "R" {
		s.agents = onboarding.Discover()
		for _, a := range s.agents {
			if a.ID == s.setupAgent.ID {
				s.setupAgent = a
			}
		}
		s.mode = providerReady
		if s.setupAgent.Path == "" {
			s.mode = installConsent
		}
		s.setupError = ""
		return nil, false
	}
	if s.mode == setupFailure {
		if key == "r" {
			if s.setupStage == "install" {
				s.mode, s.setupError = installConsent, ""
			} else {
				s.mode, s.setupError = providerReady, ""
			}
		}
		if key == "a" {
			s.openSetup()
		}
		return nil, false
	}
	stage := ""
	if s.mode == installConsent && key == "enter" {
		stage = "install"
	}
	if s.mode == providerReady {
		if key == "v" {
			stage = "verify"
		}
		if key == "l" {
			stage = "login"
		}
		if key == "enter" {
			s.agent, s.mode, s.input = s.setupAgent.ID, newCWD, s.target.CWD
			if s.input == "" {
				s.input = s.defaultCWD
			}
		}
	}
	if stage != "" {
		return &action{kind: setupInvoke, setupStage: stage}, false
	}
	return nil, false
}

func (s *state) setupLines() []string {
	if s.mode == agentSetup {
		lines := []string{"Agent setup on local machine", "PATH detection only; no network, execution or provider-auth check.", ""}
		for i, a := range s.agents {
			found := a.Path
			if found == "" {
				found = "not installed"
			}
			lines = append(lines, fmt.Sprintf("%d  %s: %s", i+1, a.Name, found))
		}
		return append(lines, "", "1 Claude Code | 2 Codex | R rescan PATH | Esc skip / sessions")
	}
	a := s.setupAgent
	lines := []string{"Agent setup: " + a.Name + " on local machine", "Source: " + a.Source, ""}
	if s.mode == installConsent {
		return append(lines, "Download: selected vendor CLI and its runtime dependencies", "Exact installer:", "  "+a.Installer, "", "Run as current OS user; no automatic sudo or package-manager setup.", "Enter = consent and install this agent | Esc = cancel without changes")
	}
	if s.mode == setupFailure {
		return append(lines, "Failed stage: "+s.setupStage, clean(s.setupError), "", "r review retry (installer always needs fresh consent)", "R rescan PATH | a choose another agent | Esc sessions", "Permission failure: fix the indicated directory; no automatic sudo.", "Admitted sessions remain in inventory; do not relaunch after uncertain admission.")
	}
	if s.setupCompleted != "" {
		lines = append(lines, "Last completed native stage: "+s.setupCompleted+" (command exited 0)")
	}
	return append(lines, "Found: "+a.Path, "v verify: "+a.ID+" --version", "l native provider login: "+a.ID+" "+strings.Join(a.Login, " "), "", "Provider login uses its own account; it may require a paid plan.", "A binary/version check does not prove authentication or session readiness.", "Enter review cwd and start | Esc sessions | R rescan PATH")
}

// This human-only handoff does not capture installer/provider output or persist
// credentials. Typed clients can read the same discovery via wt setup --json.
func setupCommand(ctx context.Context, a onboarding.Agent, stage string, streams remotepkg.IO) error {
	in, iOK := streams.In.(*os.File)
	out, oOK := streams.Out.(*os.File)
	errOut, eOK := streams.ErrOut.(*os.File)
	if !iOK || !oOK || !eOK {
		return errors.New("setup requires a private interactive terminal")
	}
	if err := onboarding.PrivateTerminal(in, out, errOut); err != nil {
		return err
	}
	if stage == "login" {
		if err := onboarding.UserTerminalContext(); err != nil {
			return err
		}
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return errors.New("installer handoff supports Linux and macOS; use the vendor's platform guide")
	}
	// Re-select from the fixed catalog so neither a display field nor PATH result
	// can inject a shell command. PATH changes require fresh resolution.
	var spec onboarding.Agent
	for _, candidate := range onboarding.Discover() {
		if candidate.ID == a.ID {
			spec = candidate
		}
	}
	if spec.ID == "" {
		return errors.New("unsupported setup agent")
	}
	deadline := 10 * time.Minute
	if stage == "verify" {
		deadline = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var cmd *exec.Cmd
	switch stage {
	case "install":
		// pipefail preserves a failed download even when the receiving shell exits 0.
		cmd = exec.Command("bash", "-o", "pipefail", "-c", spec.Installer)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	case "verify", "login":
		if spec.Path == "" {
			return errors.New("binary absent on PATH; rescan after updating PATH")
		}
		if stage == "login" && config.Channel() == "preview" {
			return errors.New("preview login requires prepared provider state; use wt-preview provider login claude or the provider setup-guide")
		}
		args := []string{"--version"}
		if stage == "login" {
			args = spec.Login
		}
		cmd = exec.CommandContext(ctx, spec.Path, args...)
	default:
		return errors.New("unsupported setup stage")
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = streams.In, streams.Out, streams.ErrOut
	var runErr error
	if stage == "install" {
		// Vendor install scripts are noninteractive after consent. Isolate their
		// group so cancellation cannot leave a download/install running on retry.
		cmd.Stdin = nil
		runErr = runActionProcess(ctx, cmd.Start, cmd.Wait, func(sig syscall.Signal) error { return syscall.Kill(-cmd.Process.Pid, sig) })
	} else {
		runErr = cmd.Run()
	}
	if err := runErr; err != nil {
		return fmt.Errorf("%s failed (%v); inspect the native terminal output, then review retry", stage, err)
	}
	return nil
}
