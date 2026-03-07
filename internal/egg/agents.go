package egg

import "runtime"

// AgentProfile declares what an agent needs from the host system.
// The sandbox merges these into the egg config automatically so users
// don't need to know agent internals (e.g. where Claude stores config).
type AgentProfile struct {
	Domains       []string // network domains needed (empty = no network)
	EnvVars       []string // required env var names (merged from host)
	PlatformEnv   []string // platform-specific env vars (e.g. macOS Keychain access)
	WriteDirs     []string // relative to $HOME, need write access
	WriteRegex    []string // dirs needing UseRegex (e.g. ".claude" covers .claude.json)
	SettingsFile  string   // agent config file relative to HOME (e.g. ".claude/settings.json")
	SessionDir    string   // agent session storage relative to $HOME (e.g. ".claude/projects")
	ResumeFlag    string   // CLI flag for resuming (e.g. "--resume")
	SessionIDFlag string   // CLI flag for controlling session ID (e.g. "--session-id")
}

// macOSKeychainEnv are env vars required for Apple Keychain access.
// Claude Code stores auth tokens in the macOS Keychain via Security framework,
// which needs XPC and CoreFoundation env vars to function.
var macOSKeychainEnv = []string{
	"XPC_SERVICE_NAME",
	"XPC_FLAGS",
	"__CFBundleIdentifier",
}

var agentProfiles = map[string]AgentProfile{
	"claude": {
		Domains:       []string{"*.anthropic.com", "*.claude.com", "sentry.io", "statsigapi.net"},
		EnvVars:       []string{"ANTHROPIC_API_KEY"},
		WriteDirs:     []string{".cache/claude"},
		WriteRegex:    []string{".claude"},
		SettingsFile:  ".claude/settings.json",
		SessionDir:    ".claude/projects",
		ResumeFlag:    "--resume",
		SessionIDFlag: "--session-id",
	},
	"codex": {
		Domains:       []string{"api.openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com"},
		EnvVars:       []string{"OPENAI_API_KEY"},
		WriteDirs:     []string{".codex"},
		SettingsFile:  ".codex/settings.json",
		SessionDir:    ".codex/sessions",
		ResumeFlag:    "resume",
	},
	"cursor": {
		Domains:      []string{"api.anthropic.com", "api.openai.com", "*.cursor.sh"},
		EnvVars:      []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		WriteDirs:    []string{".cursor", ".config", "Library/Caches/cursor-compile-cache"},
		SettingsFile: ".cursor/cli-config.json",
		ResumeFlag:   "--resume",
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
	"opencode": {
		Domains:    []string{"*.anthropic.com", "*.openai.com", "*.googleapis.com"},
		EnvVars:    []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"},
		WriteDirs:  []string{".opencode"},
		SessionDir: ".opencode/sessions",
	},
}

// Profile returns the agent profile for the given agent name.
// Unknown agents get a restrictive default (no network, no extra dirs).
// Platform-specific env vars are injected based on runtime.GOOS.
func Profile(agent string) AgentProfile {
	p, ok := agentProfiles[agent]
	if !ok {
		return AgentProfile{}
	}
	// On macOS, agents that use Keychain (claude, codex, cursor) need
	// XPC/CoreFoundation env vars for Security.framework access.
	if runtime.GOOS == "darwin" {
		switch agent {
		case "claude", "codex", "cursor", "opencode":
			p.PlatformEnv = macOSKeychainEnv
		}
	}
	return p
}
