package egg

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

// These contracts were checked against the installed packages, including
// Gemini's settings merge/schema and OpenCode's embedded plugin implementation.
// Unverified versions fail closed rather than assuming event compatibility.
var providerLifecycleVersions = map[string]string{"gemini": "0.34.0", "opencode": "1.18.13"}

var providerLifecycleCapabilityCache = struct {
	sync.Mutex
	byPath map[string]codexLifecycleCapability
}{byPath: make(map[string]codexLifecycleCapability)}

// Probe policy is assembled from the final session mounts before executing any
// provider. Trust is checked on every call, including cached capabilities.
type lifecycleProbePolicy struct {
	cwd    string
	mounts []sandbox.Mount
}

func lifecycleProbePath(binary string, policy lifecycleProbePolicy) (string, string) {
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", "native lifecycle binary could not be resolved"
	}
	// Do not turn a relative PATH result into an apparent trusted host binary.
	if !filepath.IsAbs(path) {
		return "", "native lifecycle binary path is not absolute"
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) {
		return "", "native lifecycle binary real path could not be resolved"
	}
	cwd := policy.cwd
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return "", "native lifecycle session cwd could not be resolved"
		}
	}
	roots := append([]sandbox.Mount{{Source: cwd}}, policy.mounts...)
	for _, mount := range roots {
		if mount.ReadOnly {
			continue
		}
		root, err := filepath.Abs(mount.Source)
		if err == nil {
			root, err = filepath.EvalSymlinks(root)
		}
		if err != nil {
			return "", "native lifecycle writable root could not be resolved"
		}
		candidate := path
		if runtime.GOOS == "darwin" {
			candidate, root = strings.ToLower(candidate), strings.ToLower(root)
		}
		within := candidate == root || strings.HasPrefix(candidate, strings.TrimRight(root, string(filepath.Separator))+string(filepath.Separator))
		if mount.UseRegex {
			within = strings.HasPrefix(candidate, root)
		}
		if within {
			return "", "native lifecycle binary is inside the session cwd or a writable sandbox root"
		}
	}
	return path, ""
}

// Include the backend's implicit temporary/keychain write allowances. Without
// write isolation (or in outer-boundary mode), no host probe is safe to assume.
func sessionLifecycleProbePolicy(cwd string, mounts []sandbox.Mount, sandboxed bool) lifecycleProbePolicy {
	policy := lifecycleProbePolicy{cwd: cwd, mounts: append([]sandbox.Mount(nil), mounts...)}
	writable := false
	for _, mount := range mounts {
		writable = writable || !mount.ReadOnly
	}
	if !sandboxed || !writable {
		policy.mounts = append(policy.mounts, sandbox.Mount{Source: "/"})
		return policy
	}
	policy.mounts = append(policy.mounts, sandbox.Mount{Source: os.TempDir()})
	if runtime.GOOS == "darwin" {
		policy.mounts = append(policy.mounts, sandbox.Mount{Source: "/private/tmp"})
		if home, err := os.UserHomeDir(); err == nil {
			keychains := filepath.Join(home, "Library", "Keychains")
			if _, err := os.Stat(keychains); err == nil {
				policy.mounts = append(policy.mounts, sandbox.Mount{Source: keychains})
			}
		}
	}
	return policy
}

func providerLifecycleSupported(agent, binary string, policy lifecycleProbePolicy) (bool, string) {
	verified, ok := providerLifecycleVersions[agent]
	if !ok {
		return false, ""
	}
	path, reason := lifecycleProbePath(binary, policy)
	if reason != "" {
		return false, reason
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, "native lifecycle binary version could not be checked"
	}
	providerLifecycleCapabilityCache.Lock()
	defer providerLifecycleCapabilityCache.Unlock()
	key := agent + ":" + path
	if cached, ok := providerLifecycleCapabilityCache.byPath[key]; ok && cached.modTime.Equal(info.ModTime()) {
		return cached.supported, ""
	}
	// Gemini --version performs startup housekeeping before parsing flags.
	// Probe from an empty home/workspace so it cannot clean real session data.
	probeDir, err := os.MkdirTemp("", "wt-lifecycle-version-")
	if err != nil {
		return false, "native lifecycle binary version could not be checked"
	}
	defer func() { _ = os.RemoveAll(probeDir) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Dir = probeDir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + probeDir,
		"XDG_CONFIG_HOME=" + probeDir, "XDG_DATA_HOME=" + probeDir, "XDG_STATE_HOME=" + probeDir,
		"XDG_CACHE_HOME=" + probeDir, "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1"}
	output, err := cmd.Output()
	supported := err == nil && strings.TrimSpace(string(output)) == verified
	providerLifecycleCapabilityCache.byPath[key] = codexLifecycleCapability{modTime: info.ModTime(), supported: supported}
	return supported, ""
}

func providerLifecycleHookDir(agent, home, sessionID string) string {
	base := ".gemini"
	if agent == "opencode" {
		base = filepath.Join(".local", "share", "opencode")
	}
	return filepath.Join(home, base, "wingthing-events", sessionID)
}

func prepareCodexLifecycle(rc RunConfig, binary, eggDir string, args []string, home string, policy lifecycleProbePolicy) ([]string, error) {
	if rc.Agent != "codex" || len(rc.Command) != 0 {
		return args, nil
	}
	supported, reason := codexLifecycleSupported(binary, policy)
	if reason != "" {
		return args, RecordSessionProcessEvent(eggDir, "lifecycle_unavailable", "unknown", reason)
	}
	if !supported {
		return args, nil
	}
	return CodexLifecycleArgs(args, home, filepath.Base(eggDir))
}

func prepareProviderLifecycle(rc RunConfig, binary, eggDir string, args []string, env map[string]string, policy lifecycleProbePolicy) error {
	if len(rc.Command) != 0 {
		return nil
	}
	supported, reason := providerLifecycleSupported(rc.Agent, binary, policy)
	if reason != "" {
		return RecordSessionProcessEvent(eggDir, "lifecycle_unavailable", "unknown", reason)
	}
	if !supported {
		return nil
	}
	home, sessionID := env["HOME"], filepath.Base(eggDir)
	if home == "" || !validLifecycleID(sessionID) {
		return errors.New("provider home and exact egg identity required for native hooks")
	}
	var err error
	switch rc.Agent {
	case "gemini":
		reason, err = prepareGeminiLifecycleEnv(home, sessionID, env)
	case "opencode":
		for _, arg := range args {
			if arg == "--" {
				break
			}
			if arg == "--pure" || arg == "--pure=true" {
				return nil
			}
		}
		id := rc.ProviderSessionID
		if id == "" {
			id = rc.ResumeSessionID
		}
		reason, err = prepareOpenCodeLifecycleEnv(home, sessionID, id, env)
	}
	if reason != "" {
		return RecordSessionProcessEvent(eggDir, "lifecycle_unavailable", "unknown", reason)
	}
	return err
}

var geminiLifecycleEvents = []string{"SessionStart", "BeforeAgent", "BeforeTool", "AfterTool", "BeforeModel", "AfterAgent", "Notification", "SessionEnd"}

func geminiSystemSettingsPath(env map[string]string) string {
	if path := env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"]; path != "" {
		return path
	}
	if runtime.GOOS == "darwin" {
		return "/Library/Application Support/GeminiCli/settings.json"
	}
	return "/etc/gemini-cli/settings.json"
}

// Gemini's additive invocation layer contains only our hooks. Existing system
// defaults may contain credentials outside the session's readable filesystem.
func prepareGeminiLifecycleEnv(home, sessionID string, env map[string]string) (string, error) {
	if _, set := env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"]; set {
		return "Gemini system defaults override is set; native hook injection skipped", nil
	}
	defaultsPath := filepath.Join(filepath.Dir(geminiSystemSettingsPath(env)), "system-defaults.json")
	if _, err := os.Lstat(defaultsPath); !errors.Is(err, os.ErrNotExist) {
		return "Gemini system defaults exist or cannot be checked; native hook injection skipped", nil
	}
	spool := providerLifecycleHookDir("gemini", home, sessionID)
	if err := os.MkdirAll(spool, 0700); err != nil {
		return "", err
	}
	hooks := map[string]any{}
	for _, event := range geminiLifecycleEvents {
		hooks[event] = []any{map[string]any{"matcher": "*", "hooks": []any{
			map[string]any{"type": "command", "name": "wingthing-lifecycle", "command": lifecycleHookCommand(spool), "timeout": 3000},
		}}}
	}
	data, err := json.Marshal(map[string]any{"hooks": hooks})
	if err != nil {
		return "", err
	}
	path := filepath.Join(spool, "settings") // not a published .json hook
	if err = atomicWritePrivate(path, data); err != nil {
		return "", err
	}
	env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] = path
	return "", nil
}

func prepareOpenCodeLifecycleEnv(home, sessionID, providerID string, env map[string]string) (string, error) {
	settings := map[string]json.RawMessage{}
	if content := env["OPENCODE_CONFIG_CONTENT"]; content != "" {
		var err error
		settings, err = lifecycleConfigObject([]byte(content))
		if err != nil {
			return "OpenCode invocation config could not be parsed; native hook injection skipped", nil
		}
	}
	var plugins []json.RawMessage
	if raw, ok := settings["plugin"]; ok {
		if err := json.Unmarshal(raw, &plugins); err != nil {
			return "OpenCode invocation plugins are not an array; native hook injection skipped", nil
		}
	}
	spool := providerLifecycleHookDir("opencode", home, sessionID)
	if err := os.MkdirAll(spool, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(spool, "lifecycle.mjs")
	dirJSON, _ := json.Marshal(spool)
	idJSON, _ := json.Marshal(providerID)
	code := strings.NewReplacer("__SPOOL__", string(dirJSON), "__PROVIDER_ID__", string(idJSON)).Replace(openCodeLifecyclePlugin)
	if err := atomicWritePrivate(path, []byte(code)); err != nil {
		return "", err
	}
	plugin, _ := json.Marshal((&url.URL{Scheme: "file", Path: path}).String())
	settings["plugin"], _ = json.Marshal(append(plugins, plugin))
	data, err := json.Marshal(settings)
	if err == nil {
		env["OPENCODE_CONFIG_CONTENT"] = string(data)
	}
	return "", err
}

// OpenCode accepts JSONC. Blank comments and trailing commas without changing
// quoted URLs, shell commands, escapes or other values; JSON validates the rest.
func lifecycleConfigObject(data []byte) (map[string]json.RawMessage, error) {
	data = append([]byte(nil), data...)
	quoted, escaped := false, false
	for i := 0; i < len(data); i++ {
		if quoted {
			if escaped {
				escaped = false
			} else if data[i] == '\\' {
				escaped = true
			} else if data[i] == '"' {
				quoted = false
			}
			continue
		}
		if data[i] == '"' {
			quoted = true
		} else if data[i] == '/' && i+1 < len(data) && (data[i+1] == '/' || data[i+1] == '*') {
			block := data[i+1] == '*'
			data[i], data[i+1] = ' ', ' '
			i += 2
			for ; i < len(data); i++ {
				if !block && (data[i] == '\n' || data[i] == '\r') {
					break
				}
				if block && data[i] == '*' && i+1 < len(data) && data[i+1] == '/' {
					data[i], data[i+1] = ' ', ' '
					i++
					block = false
					break
				}
				if data[i] != '\n' && data[i] != '\r' {
					data[i] = ' '
				}
			}
			if block {
				return nil, errors.New("unterminated config comment")
			}
		}
	}
	quoted, escaped = false, false
	for i := 0; i < len(data); i++ {
		if quoted {
			if escaped {
				escaped = false
			} else if data[i] == '\\' {
				escaped = true
			} else if data[i] == '"' {
				quoted = false
			}
			continue
		}
		if data[i] == '"' {
			quoted = true
		} else if data[i] == ',' {
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			// A preceding value is required: {,}, [,] and doubled commas
			// remain invalid rather than being repaired into valid JSON.
			previous := i - 1
			for previous >= 0 && (data[previous] == ' ' || data[previous] == '\t' || data[previous] == '\n' || data[previous] == '\r') {
				previous--
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') && previous >= 0 && !strings.ContainsRune("[{,:", rune(data[previous])) {
				data[i] = ' '
			}
		}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("lifecycle config must be a JSON object")
	}
	return object, nil
}
