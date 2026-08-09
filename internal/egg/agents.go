package egg

import "runtime"

// AgentProfile declares what an agent needs from the host system.
// The sandbox merges these into the egg config automatically so users
// don't need to know agent internals (e.g. where Claude stores config).
type AgentProfile struct {
	Domains       []string          // network domains needed (empty = no network)
	EnvVars       []string          // required env var names (merged from host)
	SetEnv        map[string]string // env vars forced to a default (host/config still wins)
	PlatformEnv   []string          // platform-specific env vars (e.g. macOS Keychain access)
	WriteDirs     []string          // relative to $HOME, need write access
	WriteRegex    []string          // dirs needing UseRegex (e.g. ".claude" covers .claude.json)
	SettingsFile  string            // agent config file relative to HOME (e.g. ".claude/settings.json")
	SessionDir    string            // agent session storage relative to $HOME (e.g. ".claude/projects")
	ResumeFlag    string            // CLI flag for resuming (e.g. "--resume")
	SessionIDFlag string            // CLI flag for controlling session ID (e.g. "--session-id")
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
		Domains: []string{"*.anthropic.com", "*.claude.com", "sentry.io", "statsigapi.net", "localhost", "127.0.0.1"},
		EnvVars: []string{
			"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
			"WT_PROVIDER_BASE_URL",
		},
		// Nothing to force here. 2.1.150+ enables mouse reporting, which we want — it is
		// what makes claude parse the wheel events the browser terminal sends. The enable
		// never reaches xterm (see stripMouseTracking), so the terminal never captures the
		// mouse and no click events are generated for claude to act on, which is why
		// CLAUDE_CODE_DISABLE_MOUSE_CLICKS is unnecessary. Do NOT set
		// CLAUDE_CODE_DISABLE_MOUSE: it kills reporting outright, wheel included, and it
		// takes precedence over every other mouse flag.
		WriteDirs:     []string{".cache/claude"},
		WriteRegex:    []string{".claude"},
		SettingsFile:  ".claude/settings.json",
		SessionDir:    ".claude/projects",
		ResumeFlag:    "--resume",
		SessionIDFlag: "--session-id",
	},
	"codex": {
		Domains:      []string{"api.openai.com", "*.openai.com", "chatgpt.com", "*.chatgpt.com", "localhost", "127.0.0.1"},
		EnvVars:      []string{"OPENAI_API_KEY", "CODEX_HOME", "WT_PROVIDER_BASE_URL"},
		WriteDirs:    []string{".codex"},
		SettingsFile: ".codex/settings.json",
		SessionDir:   ".codex/sessions",
		ResumeFlag:   "resume",
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
		Domains:    []string{"*.googleapis.com", "*.google.com", "*.googleusercontent.com", "generativelanguage.googleapis.com", "localhost", "127.0.0.1"},
		EnvVars:    []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GEMINI_BASE_URL", "GOOGLE_GENAI_API_VERSION", "GEMINI_CLI_TRUST_WORKSPACE", "WT_PROVIDER_BASE_URL"},
		WriteDirs:  []string{".gemini"},
		SessionDir: ".gemini/tmp",
		ResumeFlag: "--resume",
	},
	"hermes": {
		// Hermes is provider- and tool-gateway-agnostic. Its configured target
		// may be any cloud or local OpenAI-compatible endpoint, so a fixed list
		// would create a false security boundary. The capability is explicit.
		Domains: []string{"*"},
		EnvVars: []string{
			"HERMES_HOME", "NOUS_API_KEY", "OPENROUTER_API_KEY", "OPENAI_API_KEY",
			"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
			"WT_HERMES_TOOLSETS",
			"WT_PROVIDER_BASE_URL",
		},
		WriteDirs:    []string{".hermes"},
		SettingsFile: ".hermes/config.yaml",
		SessionDir:   ".hermes",
		ResumeFlag:   "--resume",
	},
	"opencode": {
		Domains:      []string{"*.anthropic.com", "*.openai.com", "*.googleapis.com", "opencode.ai", "*.opencode.ai", "models.dev", "localhost", "127.0.0.1"},
		EnvVars:      []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENCODE_CONFIG", "OPENCODE_CONFIG_CONTENT", "OLLAMA_HOST", "WT_PROVIDER_BASE_URL"},
		WriteDirs:    []string{".config/opencode", ".local/share/opencode", ".local/state/opencode", ".cache/opencode"},
		SettingsFile: ".config/opencode/opencode.json",
		SessionDir:   ".local/share/opencode",
		ResumeFlag:   "--session",
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
