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
	ProviderSubstitution bool   // accepts a non-vendor/local provider endpoint without patching the harness
	ReleaseCanary        bool   // covered by the opt-in provider-substitution release matrix
	MaxParallel          int    // 0 means no agent-specific limit beyond the caller's budget
	StatusSource         string // native interactive lifecycle source, or none
	StatusReason         string // one-line limits or reason native status is unavailable
}

var definitions = []Definition{
	{
		Name:                 "claude",
		Command:              "claude",
		UnattendedArgs:       []string{"--dangerously-skip-permissions"},
		ResumeFlag:           "--resume",
		ProviderSubstitution: true,
		ReleaseCanary:        true,
		StatusSource:         "claude_hook",
		StatusReason:         "Native Claude invocation hooks; status is unknown until a hook is observed",
	},
	{
		Name:    "codex",
		Command: "codex",
		// Codex removed --full-auto from the interactive CLI (gone by 0.147.0);
		// passing it makes the TUI exit immediately with a usage error. This is
		// also what the headless adapter already sends: Codex runs inside
		// Wingthing's sandbox, so its own inner sandbox and approvals are what
		// we are turning off, exactly as claude gets --dangerously-skip-permissions.
		UnattendedArgs:       []string{"--dangerously-bypass-approvals-and-sandbox"},
		ResumeFlag:           "resume",
		ResumeSubcommand:     true,
		ProviderSubstitution: true,
		ReleaseCanary:        true,
		StatusSource:         "codex_hook",
		StatusReason:         "Native Codex hooks require codex-cli >= 0.159.3; status is unknown until a hook is observed",
	},
	{
		Name:           "cursor",
		Command:        "agent",
		UnattendedArgs: []string{"--yolo"},
		ResumeFlag:     "--resume",
		StatusSource:   "none",
		StatusReason:   "Cursor agent 2026.02.13-41ac335 loads hooks only from config files and has no permission-prompt hook or invocation override",
	},
	{
		Name:                 "gemini",
		Command:              "gemini",
		UnattendedArgs:       []string{"--yolo"},
		ResumeFlag:           "--resume",
		ProviderSubstitution: true,
		ReleaseCanary:        true,
		StatusSource:         "gemini_hook",
		StatusReason:         "Native Gemini hooks require verified CLI 0.34.0, enabled hooks and a trusted workspace; status is unknown until a hook is observed",
	},
	{
		Name:                 "hermes",
		Command:              "hermes",
		UnattendedArgs:       []string{"--yolo"},
		ResumeFlag:           "--resume",
		ProviderSubstitution: true,
		ReleaseCanary:        true,
		StatusSource:         "none",
		StatusReason:         "Hermes has no verified invocation-scoped native lifecycle adapter",
	},
	{
		Name:                 "ollama",
		Command:              "ollama",
		InteractiveArgs:      []string{"run", DefaultOllamaModel},
		ProviderSubstitution: true,
		ReleaseCanary:        true,
		StatusSource:         "none",
		StatusReason:         "Ollama has no verified native interactive lifecycle hook mechanism",
	},
	{
		Name:                 "opencode",
		Command:              "opencode",
		UnattendedArgs:       []string{"--auto"},
		ResumeFlag:           "--session",
		ProviderSubstitution: true,
		ReleaseCanary:        true,
		StatusSource:         "opencode_hook",
		StatusReason:         "Native OpenCode plugins require verified CLI 1.18.13 without --pure; status is unknown until a hook is observed",
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
//
// extra is passed through verbatim after everything Wingthing generates, so a
// caller can select a model or set any other agent flag without Wingthing
// having to model that agent's options. Later flags win in every supported CLI,
// so appending is what makes the passthrough an override.
func InteractiveInvocation(name string, unattended bool, resumeSessionID string, extra ...string) (string, []string, bool) {
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
	// Copy rather than alias: the caller's slice must not be reachable from the
	// returned argv, and the returned argv must not be reachable from theirs.
	args = append(args, extra...)
	return definition.Command, args, true
}
