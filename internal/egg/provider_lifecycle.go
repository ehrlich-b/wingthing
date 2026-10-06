package egg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// These contracts were checked against the installed packages, including
// Gemini's settings merge/schema and OpenCode's embedded plugin implementation.
// Unverified versions fail closed rather than assuming event compatibility.
var providerLifecycleVersions = map[string]string{"gemini": "0.34.0", "opencode": "1.18.13"}

var providerLifecycleCapabilityCache = struct {
	sync.Mutex
	byPath map[string]codexLifecycleCapability
}{byPath: make(map[string]codexLifecycleCapability)}

func providerLifecycleSupported(agent, binary string) bool {
	verified, ok := providerLifecycleVersions[agent]
	if !ok {
		return false
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return false
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	providerLifecycleCapabilityCache.Lock()
	defer providerLifecycleCapabilityCache.Unlock()
	key := agent + ":" + path
	if cached, ok := providerLifecycleCapabilityCache.byPath[key]; ok && cached.modTime.Equal(info.ModTime()) {
		return cached.supported
	}
	// Gemini --version performs startup housekeeping before parsing flags.
	// Probe from an empty home/workspace so it cannot clean real session data.
	probeDir, err := os.MkdirTemp("", "wt-lifecycle-version-")
	if err != nil {
		return false
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
	return supported
}

func providerLifecycleHookDir(agent, home, sessionID string) string {
	base := ".gemini"
	if agent == "opencode" {
		base = filepath.Join(".local", "share", "opencode")
	}
	return filepath.Join(home, base, "wingthing-events", sessionID)
}

func prepareProviderLifecycle(rc RunConfig, binary, eggDir string, args []string, env map[string]string) error {
	if len(rc.Command) != 0 || !providerLifecycleSupported(rc.Agent, binary) {
		return nil
	}
	home, sessionID := env["HOME"], filepath.Base(eggDir)
	if home == "" || !validLifecycleID(sessionID) {
		return errors.New("provider home and exact egg identity required for native hooks")
	}
	switch rc.Agent {
	case "gemini":
		return prepareGeminiLifecycleEnv(home, sessionID, env)
	case "opencode":
		for _, arg := range args {
			if arg == "--" {
				break
			}
			if arg == "--pure" || arg == "--pure=true" {
				return nil // explicit plugin opt-out remains effective
			}
		}
		id := rc.ProviderSessionID
		if id == "" {
			id = rc.ResumeSessionID
		}
		return prepareOpenCodeLifecycleEnv(home, sessionID, id, env)
	}
	return nil
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

// Gemini has no settings flag. Its system-defaults env override is an additive
// invocation layer: hook arrays concatenate with user/workspace/system hooks,
// while their hooksConfig.enabled=false still wins. Preserve existing defaults.
func prepareGeminiLifecycleEnv(home, sessionID string, env map[string]string) error {
	defaultsPath := env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"]
	if defaultsPath == "" {
		defaultsPath = filepath.Join(filepath.Dir(geminiSystemSettingsPath(env)), "system-defaults.json")
	}
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(defaultsPath)
	if err == nil {
		settings, err = lifecycleConfigObject(data)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Gemini system defaults: %w", err)
	}
	spool := providerLifecycleHookDir("gemini", home, sessionID)
	if err = os.MkdirAll(spool, 0700); err != nil {
		return err
	}
	hooks := map[string]json.RawMessage{}
	if raw, ok := settings["hooks"]; ok {
		if err = json.Unmarshal(raw, &hooks); err != nil || hooks == nil {
			return errors.New("Gemini defaults hooks must be an object")
		}
	}
	for _, event := range geminiLifecycleEvents {
		var definitions []json.RawMessage
		if raw, ok := hooks[event]; ok {
			if err = json.Unmarshal(raw, &definitions); err != nil {
				return fmt.Errorf("Gemini %s hooks must be an array: %w", event, err)
			}
		}
		definition, _ := json.Marshal(map[string]any{"matcher": "*", "hooks": []any{
			map[string]any{"type": "command", "name": "wingthing-lifecycle", "command": lifecycleHookCommand(spool), "timeout": 3000},
		}})
		definitions = append(definitions, definition)
		hooks[event], _ = json.Marshal(definitions)
	}
	settings["hooks"], _ = json.Marshal(hooks)
	data, err = json.Marshal(settings)
	if err != nil {
		return err
	}
	path := filepath.Join(spool, "settings") // not a published .json hook
	if err = atomicWritePrivate(path, data); err != nil {
		return err
	}
	env["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] = path
	return nil
}

func prepareOpenCodeLifecycleEnv(home, sessionID, providerID string, env map[string]string) error {
	settings := map[string]json.RawMessage{}
	if content := env["OPENCODE_CONFIG_CONTENT"]; content != "" {
		var err error
		settings, err = lifecycleConfigObject([]byte(content))
		if err != nil {
			return fmt.Errorf("read OpenCode invocation config: %w", err)
		}
	}
	var plugins []json.RawMessage
	if raw, ok := settings["plugin"]; ok {
		if err := json.Unmarshal(raw, &plugins); err != nil {
			return fmt.Errorf("OpenCode plugins must be an array: %w", err)
		}
	}
	spool := providerLifecycleHookDir("opencode", home, sessionID)
	if err := os.MkdirAll(spool, 0700); err != nil {
		return err
	}
	path := filepath.Join(spool, "lifecycle.mjs")
	dirJSON, _ := json.Marshal(spool)
	idJSON, _ := json.Marshal(providerID)
	code := strings.NewReplacer("__SPOOL__", string(dirJSON), "__PROVIDER_ID__", string(idJSON)).Replace(openCodeLifecyclePlugin)
	if err := atomicWritePrivate(path, []byte(code)); err != nil {
		return err
	}
	plugin, _ := json.Marshal((&url.URL{Scheme: "file", Path: path}).String())
	settings["plugin"], _ = json.Marshal(append(plugins, plugin))
	data, err := json.Marshal(settings)
	if err == nil {
		env["OPENCODE_CONFIG_CONTENT"] = string(data)
	}
	return err
}

// Both installed providers accept JSON with comments. Strip only comments,
// leaving quoted URLs, shell commands, escapes and all other values intact.
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
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("lifecycle config must be a JSON object")
	}
	return object, nil
}
