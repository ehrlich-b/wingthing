// Package sessionrole holds wing-owned session metadata and role context.
package sessionrole

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/daemonctl"
	"golang.org/x/sys/unix"
)

const metadataFile = "session.role.json"
const MaxPromptBytes = 8192

type Metadata struct {
	Pinned bool   `json:"pinned,omitempty"`
	Role   string `json:"role,omitempty"`
}

func Validate(role string) error {
	if role == "" {
		return nil
	}
	if len(role) > 64 {
		return errors.New("role must be at most 64 characters")
	}
	for i, r := range role {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_'))) {
			return errors.New("role must use lowercase letters, digits, '-' or '_', starting with a letter or digit")
		}
	}
	return nil
}

func Read(dir string) (Metadata, error) {
	var m Metadata
	f, err := openRegular(filepath.Join(dir, metadataFile))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil {
		return m, err
	}
	if len(data) > 1024 {
		return m, errors.New("session role metadata exceeds 1 KiB")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, Validate(m.Role)
}

func Write(dir string, m Metadata) error {
	if err := Validate(m.Role); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return daemonctl.WriteAtomicMetadataFile(filepath.Join(dir, metadataFile), data, 0600)
}

// Prompt is read by the host before entering the sandbox. The bounded snapshot
// goes in scoped settings, so the provider needs no additional filesystem grant.
func Prompt(stateDir, role string) (string, error) {
	if err := Validate(role); err != nil {
		return "", err
	}
	if role == "" {
		return "", nil
	}
	path := filepath.Join(stateDir, "roles", role+".md")
	f, err := openRegular(path)
	if errors.Is(err, os.ErrNotExist) && role == "coordinator" {
		return "you are the coordinator node. Your role instructions belong in " + path + ". Use the wingthing MCP to inspect and coordinate agents, sessions, and messages across configured machines.", nil
	}
	if err != nil {
		return "", fmt.Errorf("read role %s: %w", role, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("role prompt must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxPromptBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > MaxPromptBytes || !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" || strings.ContainsRune(string(data), 0) {
		return "", errors.New("role prompt must be nonempty, without NUL, and at most 8 KiB")
	}
	return string(data), nil
}

func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		f.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		f.Close()
		return nil, errors.New("role data must be a regular file with one link")
	}
	return f, nil
}

// Hook returns JSON context as data, never executable role-file content.
func Hook(prompt string) map[string]any {
	payload, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "SessionStart", "additionalContext": prompt,
	}})
	quoted := "'" + strings.ReplaceAll(string(payload), "'", "'\"'\"'") + "'"
	return map[string]any{"matcher": "startup|resume|clear|compact", "hooks": []any{
		map[string]any{"type": "command", "command": "printf '%s\\n' " + quoted, "timeout": 5},
	}}
}

func AddHook(settings map[string]any, prompt string, configDir ...string) error {
	if disabled, _ := settings["disableAllHooks"].(bool); disabled {
		return errors.New("role sessions require SessionStart hooks; disableAllHooks is set")
	}
	if env, ok := settings["env"].(map[string]any); ok {
		selected := map[string]string{}
		for k, v := range env {
			if value, ok := v.(string); ok {
				selected[k] = value
			}
		}
		if err := ValidateClaudeEnvironment(selected, configDir...); err != nil {
			return err
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks["SessionStart"].([]any)
	hooks["SessionStart"] = append(entries, Hook(prompt))
	settings["hooks"] = hooks
	return nil
}

func ValidateClaudeEnvironment(env map[string]string, configDir ...string) error {
	for _, key := range []string{"CLAUDE_CODE_SIMPLE", "CLAUDE_CODE_SAFE_MODE"} {
		if value := env[key]; value == "1" || strings.EqualFold(value, "true") {
			return fmt.Errorf("role sessions require hooks and scoped MCP; %s is enabled", key)
		}
	}
	if len(configDir) > 0 && env["CLAUDE_CONFIG_DIR"] != "" && config.CanonicalProviderPath(env["CLAUDE_CONFIG_DIR"]) != config.CanonicalProviderPath(configDir[0]) {
		return errors.New("role sessions require CLAUDE_CONFIG_DIR to select their provider data directory")
	}
	return nil
}

// These provider modes skip all supplied hooks or MCP servers. They remain
// valid for ordinary sessions, but cannot satisfy a role session's contract.
func ValidateClaudeArgs(args []string) error {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--bare" || arg == "--safe-mode" || strings.HasPrefix(arg, "--bare=") || strings.HasPrefix(arg, "--safe-mode=") {
			return errors.New("role sessions require hooks and scoped MCP; --bare and --safe-mode are incompatible")
		}
	}
	return nil
}
