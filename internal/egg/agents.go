package egg

import "github.com/ehrlich-b/wingthing/internal/sandbox"

// AgentProfile declares what an agent needs from the host system.
// The sandbox merges these into the egg config automatically so users
// don't need to know agent internals (e.g. where Claude stores config).
type AgentProfile struct {
	Network    sandbox.NetworkNeed
	EnvVars    []string // required env var names (merged from host)
	WriteDirs  []string // relative to $HOME, need write access
	WriteRegex []string // dirs needing UseRegex (e.g. ".claude" covers .claude.json)
}

var agentProfiles = map[string]AgentProfile{
	"claude": {
		Network:    sandbox.NetworkHTTPS,
		EnvVars:    []string{"ANTHROPIC_API_KEY"},
		WriteDirs:  []string{".cache/claude"},
		WriteRegex: []string{".claude"},
	},
	"codex": {
		Network:   sandbox.NetworkHTTPS,
		EnvVars:   []string{"OPENAI_API_KEY"},
		WriteDirs: []string{".codex"},
	},
	"cursor": {
		Network: sandbox.NetworkHTTPS,
		EnvVars: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		WriteDirs: []string{".cursor"},
	},
	"ollama": {
		Network:   sandbox.NetworkLocal,
		WriteDirs: []string{".ollama"},
	},
	"gemini": {
		Network: sandbox.NetworkHTTPS,
		EnvVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		WriteDirs: []string{".gemini"},
	},
}

// Profile returns the agent profile for the given agent name.
// Unknown agents get a restrictive default (no network, no extra dirs).
func Profile(agent string) AgentProfile {
	if p, ok := agentProfiles[agent]; ok {
		return p
	}
	return AgentProfile{Network: sandbox.NetworkNone}
}
