package egg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// These are the main-thread events in Codex 0.159.3. Subagent events use the
// parent's session ID and must not end its turn. There is no Notification or
// MCP elicitation event; unsupported transitions cannot be inferred from a PTY.
// These hooks report readiness and prompt admission. CodexRunArgs separately
// installs notify to provide authoritative turn completion and final text.
var codexLifecycleEvents = []struct{ name, key string }{
	{"SessionStart", "session_start"},
	{"UserPromptSubmit", "user_prompt_submit"},
	{"PreToolUse", "pre_tool_use"},
	{"PermissionRequest", "permission_request"},
	{"PostToolUse", "post_tool_use"},
	{"PreCompact", "pre_compact"},
	{"PostCompact", "post_compact"},
	{"Stop", "stop"},
	{"Interrupt", "interrupt"},
	{"SessionEnd", "session_end"},
}

type codexLifecycleCapability struct {
	modTime   time.Time
	supported bool
}

var codexLifecycleCapabilityCache = struct {
	sync.Mutex
	byPath map[string]codexLifecycleCapability
}{byPath: make(map[string]codexLifecycleCapability)}

func codexLifecycleSupported(binary string) bool {
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
	// Serialize probes so concurrent launches only run --version once per version.
	codexLifecycleCapabilityCache.Lock()
	defer codexLifecycleCapabilityCache.Unlock()
	if cached, ok := codexLifecycleCapabilityCache.byPath[path]; ok && cached.modTime.Equal(info.ModTime()) {
		return cached.supported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").Output()
	version, parsed := parseCodexVersion(string(output))
	supported := err == nil && parsed
	if supported {
		// 0.159.3 is the earliest locally verified hook trust format. The
		// bypass help flag does not establish compatibility with this format.
		for i, minimum := range [3]uint64{0, 159, 3} {
			if version[i] != minimum {
				supported = version[i] > minimum
				break
			}
		}
	}
	codexLifecycleCapabilityCache.byPath[path] = codexLifecycleCapability{modTime: info.ModTime(), supported: supported}
	return supported
}

func parseCodexVersion(output string) ([3]uint64, bool) {
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[0] != "codex-cli" {
		return [3]uint64{}, false
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) != 3 {
		return [3]uint64{}, false
	}
	var version [3]uint64
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return [3]uint64{}, false
		}
		version[i] = value
	}
	return version, true
}

// CodexLifecycleArgs installs observational hooks in the session-flags layer.
// Only the exact generated definitions are trusted. Neither CODEX_HOME nor its
// config, notify, existing hook sources, or global trust policy is changed.
func CodexLifecycleArgs(args []string, home, sessionID string) ([]string, error) {
	if home == "" || !validLifecycleID(sessionID) {
		return nil, errors.New("provider home and exact egg identity required for Codex hooks")
	}
	// Respect explicit hook configuration in this same layer instead of
	// replacing a caller's definitions or enabling hooks they disabled.
	optionsEnd := len(args)
	for i, arg := range args {
		if arg == "--" {
			optionsEnd = i
			break
		}
		value := ""
		if (arg == "-c" || arg == "--config") && i+1 < len(args) {
			value = args[i+1]
		} else if strings.HasPrefix(arg, "--config=") {
			value = strings.TrimPrefix(arg, "--config=")
		} else if strings.HasPrefix(arg, "-c") {
			value = strings.TrimPrefix(arg, "-c")
		}
		key, configured, _ := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if key == "hooks" || strings.HasPrefix(key, "hooks.") || (key == "features.hooks" && strings.TrimSpace(configured) == "false") ||
			(arg == "--disable" && i+1 < len(args) && args[i+1] == "hooks") || arg == "--disable=hooks" {
			return args, nil
		}
	}
	spool := filepath.Join(home, ".codex", "wingthing-events", sessionID)
	if err := os.MkdirAll(spool, 0700); err != nil {
		return nil, err
	}
	command := lifecycleHookCommand(spool)
	out := append([]string(nil), args[:optionsEnd]...)
	var trustEntries []string
	for _, event := range codexLifecycleEvents {
		hash, err := codexLifecycleHookHash(event.key, command)
		if err != nil {
			return nil, err
		}
		definition := "hooks." + event.name + "=[{hooks=[{type=\"command\",command=" + strconv.Quote(command) + ",timeout=3}]}]"
		key := "/<session-flags>/config.toml:" + event.key + ":0:0"
		trustEntries = append(trustEntries, strconv.Quote(key)+"={trusted_hash="+strconv.Quote(hash)+"}")
		out = append(out, "-c", definition)
	}
	// CLI dotted keys split on every dot, including dots inside quoted keys.
	// An inline table preserves the synthetic source path's config.toml key.
	// Codex merges it with lower config layers, preserving existing trust entries.
	out = append(out, "-c", "hooks.state={"+strings.Join(trustEntries, ",")+"}")
	return append(out, args[optionsEnd:]...), nil
}

// Codex hashes sorted compact JSON of the normalized TOML hook identity.
// Optional unset fields are absent in TOML, and async defaults to false.
// Verified against the installed 0.159.3 hooks/list response. A provider change
// to this protocol fails closed: an unmatched hash leaves the hooks untrusted.
func codexLifecycleHookHash(eventKey, command string) (string, error) {
	identity := map[string]any{"event_name": eventKey, "hooks": []any{
		map[string]any{"type": "command", "command": command, "timeout": 3, "async": false},
	}}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(identity); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(bytes.TrimSuffix(data.Bytes(), []byte{'\n'}))), nil
}
