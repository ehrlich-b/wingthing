package agent

import (
	"reflect"
	"testing"
)

func TestInteractiveInvocation(t *testing.T) {
	tests := []struct {
		name       string
		agent      string
		unattended bool
		resume     string
		command    string
		args       []string
	}{
		{name: "claude", agent: "claude", unattended: true, resume: "c1", command: "claude", args: []string{"--dangerously-skip-permissions", "--resume", "c1"}},
		{name: "codex resume includes id", agent: "codex", unattended: true, resume: "cx1", command: "codex", args: []string{"resume", "cx1", "--full-auto"}},
		{name: "cursor", agent: "cursor", unattended: true, resume: "cu1", command: "agent", args: []string{"--yolo", "--resume", "cu1"}},
		{name: "gemini", agent: "gemini", unattended: true, resume: "g1", command: "gemini", args: []string{"--yolo", "--resume", "g1"}},
		{name: "hermes", agent: "hermes", unattended: true, resume: "h1", command: "hermes", args: []string{"--yolo", "--resume", "h1"}},
		{name: "ollama", agent: "ollama", command: "ollama", args: []string{"run", DefaultOllamaModel}},
		{name: "opencode", agent: "opencode", unattended: true, resume: "o1", command: "opencode", args: []string{"--auto", "--session", "o1"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, args, ok := InteractiveInvocation(test.agent, test.unattended, test.resume)
			if !ok {
				t.Fatalf("InteractiveInvocation(%q) not found", test.agent)
			}
			if command != test.command || !reflect.DeepEqual(args, test.args) {
				t.Fatalf("InteractiveInvocation(%q) = %q %q, want %q %q", test.agent, command, args, test.command, test.args)
			}
		})
	}
}

func TestInteractiveInvocationRejectsUnknownAgent(t *testing.T) {
	if command, args, ok := InteractiveInvocation("unknown", false, ""); ok || command != "" || args != nil {
		t.Fatalf("unknown invocation = %q %q, %v", command, args, ok)
	}
}

func TestDefinitionsReturnsIsolatedCopy(t *testing.T) {
	first := Definitions()
	first[0].InteractiveArgs = append(first[0].InteractiveArgs, "mutated")
	first[0].UnattendedArgs[0] = "mutated"

	second := Definitions()
	if reflect.DeepEqual(first[0], second[0]) {
		t.Fatal("Definitions returned shared mutable slices")
	}
}

func TestOpenCodeCatalogSerializesSharedState(t *testing.T) {
	definition, ok := LookupDefinition("opencode")
	if !ok {
		t.Fatal("opencode missing from catalog")
	}
	if definition.MaxParallel != 1 {
		t.Fatalf("opencode MaxParallel = %d, want 1", definition.MaxParallel)
	}
}

func TestProviderSubstitutionRequiresReleaseCanary(t *testing.T) {
	var got []string
	for _, definition := range Definitions() {
		if definition.ProviderSubstitution {
			got = append(got, definition.Name)
			if !definition.ReleaseCanary {
				t.Errorf("provider-substitutable agent %q has no release canary", definition.Name)
			}
		}
	}
	want := []string{"claude", "codex", "gemini", "hermes", "ollama", "opencode"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider-substitutable agents = %q, want %q", got, want)
	}
}
