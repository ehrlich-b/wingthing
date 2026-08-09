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
		{agent: "codex", command: "codex", args: []string{"resume", "codex-session", "--full-auto"}},
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
