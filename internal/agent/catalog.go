package agent

// Definition is the executable contract Wingthing uses to discover and start
// an interactive coding agent. Headless prompt adapters live beside this
// catalog, but discovery and PTY launch must share this single source of truth.
type Definition struct {
	Name                 string
	Command              string
	InteractiveArgs      []string
	UnattendedArgs       []string
	ResumeFlag           string
	ResumeSubcommand     bool
	ProviderSubstitution bool // accepts a non-vendor/local provider endpoint without patching the harness
	ReleaseCanary        bool // covered by the opt-in provider-substitution release matrix
	MaxParallel          int  // 0 means no agent-specific limit beyond the caller's budget
}

var definitions = []Definition{
	{
		Name:                 "claude",
		Command:              "claude",
		UnattendedArgs:       []string{"--dangerously-skip-permissions"},
		ResumeFlag:           "--resume",
		ProviderSubstitution: true,
		ReleaseCanary:        true,
	},
	{
		Name:                 "codex",
		Command:              "codex",
		UnattendedArgs:       []string{"--full-auto"},
		ResumeFlag:           "resume",
		ResumeSubcommand:     true,
		ProviderSubstitution: true,
		ReleaseCanary:        true,
	},
	{
		Name:           "cursor",
		Command:        "agent",
		UnattendedArgs: []string{"--yolo"},
		ResumeFlag:     "--resume",
	},
	{
		Name:                 "gemini",
		Command:              "gemini",
		UnattendedArgs:       []string{"--yolo"},
		ResumeFlag:           "--resume",
		ProviderSubstitution: true,
		ReleaseCanary:        true,
	},
	{
		Name:                 "hermes",
		Command:              "hermes",
		UnattendedArgs:       []string{"--yolo"},
		ResumeFlag:           "--resume",
		ProviderSubstitution: true,
		ReleaseCanary:        true,
	},
	{
		Name:                 "ollama",
		Command:              "ollama",
		InteractiveArgs:      []string{"run", DefaultOllamaModel},
		ProviderSubstitution: true,
		ReleaseCanary:        true,
	},
	{
		Name:                 "opencode",
		Command:              "opencode",
		UnattendedArgs:       []string{"--auto"},
		ResumeFlag:           "--session",
		ProviderSubstitution: true,
		ReleaseCanary:        true,
		// OpenCode uses one SQLite database in its XDG data directory and its
		// current CLI fails immediately when two headless instances share it.
		MaxParallel: 1,
	},
}

// Definitions returns an isolated copy of the supported-agent catalog.
func Definitions() []Definition {
	result := make([]Definition, len(definitions))
	for i, definition := range definitions {
		result[i] = definition
		result[i].InteractiveArgs = append([]string(nil), definition.InteractiveArgs...)
		result[i].UnattendedArgs = append([]string(nil), definition.UnattendedArgs...)
	}
	return result
}

// LookupDefinition resolves an agent name to its executable contract.
func LookupDefinition(name string) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			definition.InteractiveArgs = append([]string(nil), definition.InteractiveArgs...)
			definition.UnattendedArgs = append([]string(nil), definition.UnattendedArgs...)
			return definition, true
		}
	}
	return Definition{}, false
}

// InteractiveInvocation builds the executable and arguments for a PTY-backed
// agent. Resume subcommands include the requested session ID explicitly.
func InteractiveInvocation(name string, unattended bool, resumeSessionID string) (string, []string, bool) {
	definition, ok := LookupDefinition(name)
	if !ok {
		return "", nil, false
	}

	args := append([]string(nil), definition.InteractiveArgs...)
	if unattended {
		args = append(args, definition.UnattendedArgs...)
	}
	if resumeSessionID != "" && definition.ResumeFlag != "" {
		if definition.ResumeSubcommand {
			args = append([]string{definition.ResumeFlag, resumeSessionID}, args...)
		} else {
			args = append(args, definition.ResumeFlag, resumeSessionID)
		}
	}
	return definition.Command, args, true
}
