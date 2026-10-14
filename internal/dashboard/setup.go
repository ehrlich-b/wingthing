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
	"github.com/mattn/go-runewidth"
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
		if key == "A" {
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
			s.input = s.homeShort(s.target.machine, s.input)
		}
	}
	if stage != "" {
		return &action{kind: setupInvoke, setupStage: stage}, false
	}
	return nil, false
}

func (s *state) setupLines() []string {
	if s.mode == agentSetup {
		lines := []string{"Install an agent", "This machine", ""}
		nameWidth := agentNameWidth(s.agents)
		for _, a := range s.agents {
			status := "Missing"
			if a.Path != "" {
				status = "Installed"
			}
			lines = append(lines, field(a.Name, nameWidth)+"   "+status)
		}
		return lines
	}
	a := s.setupAgent
	lines := []string{a.Name + " · This machine", "Source: " + a.Source, ""}
	if s.mode == installConsent {
		return append(lines, "Install "+a.Name, "Downloads the agent and its runtime dependencies.", "", a.Installer, "", "Runs as your OS user. No automatic sudo.", "Nothing changes until you choose Install.")
	}
	if s.mode == setupFailure {
		cause := "The command failed. Its output is in your terminal."
		lower := strings.ToLower(s.setupError)
		switch {
		case strings.Contains(lower, "managed"), strings.Contains(lower, "ancestry"), strings.Contains(lower, "user terminal"), strings.Contains(lower, "context"):
			cause = "Sign in to " + a.Name + " in a normal terminal:"
		case strings.Contains(lower, "download"), strings.Contains(lower, "curl"), s.setupStage == "install" && strings.Contains(lower, "exit status 22"):
			cause = "The download failed. Check your network and the source above."
		case strings.Contains(lower, "permission"):
			cause = "Permission denied. Check you can write to the install folder."
		case strings.Contains(lower, "absent"), strings.Contains(lower, "path"):
			cause = a.ID + " is not on your PATH. Fix PATH, then press R."
		}
		lines = append(lines, setupStageLabel(s.setupStage)+" failed.", cause)
		if strings.HasPrefix(cause, "Sign in to ") {
			lines = append(lines, a.ID+" "+strings.Join(a.Login, " "))
		}
		return lines
	}
	if s.setupCompleted != "" {
		lines = append(lines, setupStageLabel(s.setupCompleted)+" finished.")
	}
	return append(lines, "Installed", "", "To sign in, run this in a normal terminal:", a.ID+" "+strings.Join(a.Login, " "))
}

func setupStageLabel(stage string) string {
	switch stage {
	case "install":
		return "Install"
	case "login":
		return "Sign-in"
	case "verify":
		return "Version check"
	}
	return "Setup"
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

func agentNameWidth(agents []onboarding.Agent) int {
	width := 0
	for _, a := range agents {
		width = max(width, runewidth.StringWidth(a.Name))
	}
	return width
}
