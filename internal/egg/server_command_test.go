package egg

import (
	"bufio"
	"context"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestFormatCommand(t *testing.T) {
	command := []string{"sh", "-c", "A=B echo '$A'"}
	if got, want := formatCommand(command), `"sh" "-c" "A=B echo '$A'"`; got != want {
		t.Fatalf("formatCommand = %q, want %q", got, want)
	}
}

func TestAgentCommandUsesSupportedAgentCatalog(t *testing.T) {
	tests := []struct {
		agent   string
		command string
		args    []string
	}{
		{agent: "gemini", command: "gemini", args: []string{"--yolo", "--resume", "gemini-session"}},
		{agent: "opencode", command: "opencode", args: []string{"--auto", "--session", "opencode-session"}},
		{agent: "codex", command: "codex", args: []string{"resume", "codex-session", "--dangerously-bypass-approvals-and-sandbox"}},
	}

	for _, test := range tests {
		t.Run(test.agent, func(t *testing.T) {
			command, args := agentCommand(test.agent, true, test.agent+"-session")
			if command != test.command || !reflect.DeepEqual(args, test.args) {
				t.Fatalf("agentCommand(%q) = %q %q, want %q %q", test.agent, command, args, test.command, test.args)
			}
		})
	}
}

// TestRunSessionAgentArgsKeepTheAgentProfile is the distinction that matters:
// AgentArgs extends the agent invocation, so the session is still an agent
// session with the agent's sandbox profile. Command replaces it entirely and
// makes the session an opaque command, which is why picking a model could not
// be expressed through Command.
func TestRunSessionAgentArgsKeepTheAgentProfile(t *testing.T) {
	name, args := agentCommand("codex", false, "", "-m", "gpt-5.6-terra")
	if name != "codex" || !reflect.DeepEqual(args, []string{"-m", "gpt-5.6-terra"}) {
		t.Fatalf("agentCommand with args = %q %q", name, args)
	}

	withArgs := RunConfig{Agent: "codex", AgentArgs: []string{"-m", "gpt-5.6-terra"}}
	gotName, gotArgs := sessionCommand(withArgs)
	if gotName != "codex" || !reflect.DeepEqual(gotArgs, []string{"-m", "gpt-5.6-terra"}) {
		t.Fatalf("sessionCommand(agent+args) = %q %q", gotName, gotArgs)
	}

	// An explicit Command still wins and is verbatim, unchanged behaviour.
	explicit := RunConfig{Agent: "codex", Command: []string{"bash", "-lc", "echo hi"}, AgentArgs: []string{"-m", "ignored"}}
	gotName, gotArgs = sessionCommand(explicit)
	if gotName != "bash" || !reflect.DeepEqual(gotArgs, []string{"-lc", "echo hi"}) {
		t.Fatalf("sessionCommand(command) = %q %q", gotName, gotArgs)
	}
}

func TestRunConfigPolicyPreservesNetworkMappingControls(t *testing.T) {
	rc := RunConfig{
		Agent: "claude", Network: []string{"api.example.test"}, LocalPorts: []int{4317},
		NetworkMode: "observe", AgentDomains: "none",
	}
	policy := runConfigPolicy(rc)
	if policy.Network.Mode != "observe" || policy.Network.AgentDomains != "none" {
		t.Fatalf("network controls = %#v", policy.Network)
	}
	if len(policy.Network.LocalPorts) != 1 || policy.Network.LocalPorts[0] != 4317 {
		t.Fatalf("local ports = %#v", policy.Network.LocalPorts)
	}
	if !RequiresSandbox(policy, rc.Agent) {
		t.Fatal("an explicit filtered domain policy must require the sandbox")
	}

	// Suppressing an agent's default domains is part of the subprocess
	// contract too. Dropping it here would silently restore provider egress.
	suppressed := runConfigPolicy(RunConfig{Agent: "claude", AgentDomains: "none"})
	resolved := ResolvePolicy(suppressed, "claude", "")
	if len(resolved.Domains) != 0 || len(resolved.Suppressed) == 0 {
		t.Fatalf("agent domain suppression was lost: policy=%#v resolved=%#v", suppressed.Network, resolved)
	}
}

func TestRunConfigNeedsExplicitOuterBoundaryMarker(t *testing.T) {
	ordinary := runConfigPolicy(RunConfig{Agent: "claude", Network: []string{"*"}})
	if !RequiresSandbox(ordinary, "claude") {
		t.Fatal("wildcard child policy silently became outer-boundary mode")
	}

	trusted := runConfigPolicy(RunConfig{Agent: "claude", Network: []string{"*"}, OuterBoundary: true})
	if RequiresSandbox(trusted, "claude") {
		t.Fatal("explicit outer-boundary child marker was not preserved")
	}
}

func TestTerminateSessionEscalatesWhenInteractiveProcessIgnoresSIGTERM(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap '' TERM; printf 'ready\\n'; while :; do :; done")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	})

	// Do not race the TERM against the shell installing its trap.
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("wait for child readiness: line=%q err=%v", line, err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := terminateSession(ctx, &Session{cmd: cmd, done: done}, 20*time.Millisecond); err != nil {
		t.Fatalf("terminate ignored SIGTERM: %v", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("terminateSession returned before the child exited")
	}
}
