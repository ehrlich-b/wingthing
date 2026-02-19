package egg

// AgentProfile declares what an agent needs from the host system.
// The sandbox merges these into the egg config automatically so users
// don't need to know agent internals (e.g. where Claude stores config).
type AgentProfile struct {
	Domains      []string // network domains needed (empty = no network)
	EnvVars      []string // required env var names (merged from host)
	WriteDirs    []string // relative to $HOME, need write access
	WriteRegex   []string // dirs needing UseRegex (e.g. ".claude" covers .claude.json)
	SettingsFile string   // agent config file relative to HOME (e.g. ".claude/settings.json")
}

var agentProfiles = map[string]AgentProfile{
	"claude": {
		Domains:      []string{"*.anthropic.com", "*.claude.com", "sentry.io", "statsigapi.net"},
		EnvVars:      []string{"ANTHROPIC_API_KEY"},
		WriteDirs:    []string{".cache/claude"},
		WriteRegex:   []string{".claude"},
		SettingsFile: ".claude/settings.json",
	},
	"codex": {
		Domains:      []string{"api.openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com"},
		EnvVars:      []string{"OPENAI_API_KEY"},
		WriteDirs:    []string{".codex"},
		SettingsFile: ".codex/settings.json",
	},
	"cursor": {
		Domains:      []string{"api.anthropic.com", "api.openai.com", "*.cursor.sh"},
		EnvVars:      []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		WriteDirs:    []string{".cursor", ".config", "Library/Caches/cursor-compile-cache"},
		SettingsFile: ".cursor/cli-config.json",
	},
	"ollama": {
		Domains:   []string{"localhost"},
		WriteDirs: []string{".ollama"},
	},
	"gemini": {
		Domains:   []string{"*.googleapis.com", "generativelanguage.googleapis.com"},
		EnvVars:   []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		WriteDirs: []string{".gemini"},
	},
}

// Profile returns the agent profile for the given agent name.
// Unknown agents get a restrictive default (no network, no extra dirs).
func Profile(agent string) AgentProfile {
	if p, ok := agentProfiles[agent]; ok {
		return p
	}
	return AgentProfile{}
}
