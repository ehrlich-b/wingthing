package egg

import (
	"reflect"
	"testing"
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
