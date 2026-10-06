package egg

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/procinfo"
	"gopkg.in/yaml.v3"
)

const ControlIsolationVersion = "3"

// v0.147.0 denies ~/.gnupg by default on both platforms. Mode 0700 alone
// cannot isolate same-UID eggs. State contains only a public directory locator.
func controlDirectory(dir string) string {
	home, _ := os.UserHomeDir()
	return controlDirectoryUnderHome(dir, home)
}

func controlDirectoryUnderHome(dir, home string) string {
	key := sha256.Sum256([]byte(config.CanonicalProviderPath(dir)))
	return filepath.Join(home, ".gnupg", "wingthing-control", fmt.Sprintf("%x", key))
}

// The locator also lets host clients with a different HOME attach to an egg.
// Bind it to the session identity before reading or cleaning external files.
func readControlDirectory(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "egg.control"))
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(data))
	home := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if !filepath.IsAbs(path) || path != controlDirectoryUnderHome(dir, home) {
		return "", fmt.Errorf("invalid egg controller directory")
	}
	return path, nil
}

// Only the runtime writes this marker, before publishing its PID. Mutable
// session metadata and wing/broker restarts cannot erase the isolation identity.
func HasCurrentControlIsolation(dir string) bool {
	path, err := readControlDirectory(dir)
	if err != nil || path != controlDirectory(dir) {
		return false
	}
	return hasControlIsolationAt(path)
}

func hasControlIsolationAt(path string) bool {
	data, err := os.ReadFile(filepath.Join(path, "isolation"))
	return err == nil && string(data) == ControlIsolationVersion+"\n"
}

func prepareControlDirectory(dir string) (string, error) {
	path := controlDirectory(dir)
	// Do not redirect credentials through an alias of a historical deny path.
	for _, ancestor := range []string{filepath.Dir(filepath.Dir(path)), filepath.Dir(path), path} {
		if err := os.Mkdir(ancestor, 0700); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(ancestor)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("controller directory must be a real directory: %s", ancestor)
		}
	}
	return path, nil
}

// Never remove an unvalidated path supplied by session metadata.
func RemoveControlCredentials(dir string) {
	path := controlDirectory(dir)
	if located, err := readControlDirectory(dir); err == nil {
		path = located
	}
	for _, ancestor := range []string{filepath.Dir(filepath.Dir(path)), filepath.Dir(path), path} {
		info, err := os.Lstat(ancestor)
		if err != nil || !info.IsDir() {
			return
		}
	}
	for _, name := range []string{"egg.token", "isolation"} {
		if err := os.Remove(filepath.Join(path, name)); err != nil && !os.IsNotExist(err) {
			log.Printf("egg: remove controller credential: %v", err)
		}
	}
	_ = os.Remove(path)
}

func MarkLegacyEggForReplacement(dir string) {
	file, err := os.OpenFile(filepath.Join(dir, "replacement-required"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return
	}
	if err != nil {
		log.Printf("egg: mark legacy session %s: %v", filepath.Base(dir), err)
		return
	}
	_, writeErr := file.WriteString("Legacy sandbox isolation: restart this session to recover its tool capability; privileged tools are not recovered. Its PTY remains available and new sessions may start.\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		log.Printf("egg: write legacy capability reason: %v %v", writeErr, closeErr)
	}
	log.Printf("egg: legacy session %s requires restart for tool capability recovery; preserving PTY", filepath.Base(dir))
}

// LegacyIsolation records exposure at admission separately from mutable PTY
// metadata and the controller marker used to authorize capability recovery.
type LegacyIsolation struct {
	Reason         string   `json:"reason"`
	LegacySessions []string `json:"legacy_sessions"`
}

func (i *LegacyIsolation) Warning() string {
	return fmt.Sprintf("Warning: isolation: degraded; %s; legacy sessions: %s. Restart those sessions to restore isolation; their tool capabilities remain withheld.", i.Reason, strings.Join(i.LegacySessions, ", "))
}

func ReadLegacyIsolation(dir string) *LegacyIsolation {
	data, err := os.ReadFile(filepath.Join(dir, "isolation-degraded"))
	var result LegacyIsolation
	if err != nil || json.Unmarshal(data, &result) != nil || result.Reason == "" || len(result.LegacySessions) == 0 {
		return nil
	}
	return &result
}

// A live egg upgrade admits new sessions by default, retaining an explicit
// warning about legacy policies. Strict mode restores the launch refusal.
func RequireLegacySecretProtection(sessionDir string, toolCapability bool) error {
	wingCfg, err := config.LoadWingConfig(filepath.Dir(filepath.Dir(sessionDir)))
	if err != nil {
		return err
	}
	isolation := InspectLegacyIsolation(sessionDir, toolCapability)
	if isolation == nil {
		return nil
	}
	if wingCfg.LegacyIsolation == config.LegacyIsolationStrict {
		return fmt.Errorf("legacy isolation strict: %s (legacy sessions: %s); restart those sessions before starting new ones", isolation.Reason, strings.Join(isolation.LegacySessions, ", "))
	}
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(isolation)
	if err != nil {
		return err
	}
	previous, _ := os.ReadFile(filepath.Join(sessionDir, "isolation-degraded"))
	if string(previous) == string(data) {
		return nil
	}
	if err := atomicWritePrivate(filepath.Join(sessionDir, "isolation-degraded"), data); err != nil {
		return err
	}
	log.Printf("session %s: %s", filepath.Base(sessionDir), isolation.Warning())
	return nil
}

// Inspect each surviving policy. Failure to inspect a legacy egg is itself a
// degraded boundary, not a reason to prevent the rest of the wing from working.
func InspectLegacyIsolation(sessionDir string, toolCapability bool) *LegacyIsolation {
	home, _ := os.UserHomeDir()
	states := []string{filepath.Join(home, ".wingthing"), filepath.Join(home, ".wingthing-preview"), filepath.Dir(filepath.Dir(sessionDir))}
	if state, err := config.StateDir(); err == nil {
		states = append(states, state)
	}
	seen := make(map[string]bool)
	legacy := make(map[string]bool)
	reasons := make(map[string]bool)
	add := func(id, reason string) {
		legacy[id] = true
		reasons[reason] = true
	}
	for _, state := range states {
		state = config.CanonicalProviderPath(state)
		if seen[state] {
			continue
		}
		seen[state] = true
		entries, err := os.ReadDir(filepath.Join(state, "eggs"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			add(state, fmt.Sprintf("cannot inspect legacy sessions: %v", err))
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(state, "eggs", entry.Name())
			if HasCurrentControlIsolation(dir) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, "egg.pid"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				add(entry.Name(), fmt.Sprintf("cannot inspect legacy egg PID: %v", err))
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || !procinfo.OwnedProcessIsAlive(pid) {
				continue
			}
			MarkLegacyEggForReplacement(dir)
			// Old macOS policies permit process-info on siblings, exposing
			// environment capabilities even when controller files are denied.
			if toolCapability && runtime.GOOS == "darwin" {
				add(entry.Name(), "legacy policies permit reading new tool capability environments")
			}
			if err := legacyDeniesControlDirectory(dir, controlDirectory(sessionDir), home); err != nil {
				add(entry.Name(), "cannot exclude new controller secrets: "+err.Error())
			}
		}
	}
	if len(legacy) == 0 {
		return nil
	}
	result := &LegacyIsolation{}
	for id := range legacy {
		result.LegacySessions = append(result.LegacySessions, id)
	}
	var messages []string
	for reason := range reasons {
		messages = append(messages, reason)
	}
	sort.Strings(result.LegacySessions)
	sort.Strings(messages)
	result.Reason = strings.Join(messages, "; ")
	return result
}

func legacyDeniesControlDirectory(dir, target, home string) error {
	target = config.CanonicalProviderPath(target)
	client, err := Dial(filepath.Join(dir, "egg.sock"), filepath.Join(dir, "egg.token"))
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state, err := client.Status(ctx)
	if err != nil {
		return err
	}
	var policy EggConfig
	if state.RenderedConfig == "" {
		return fmt.Errorf("surviving sandbox policy is unavailable")
	}
	if err := yaml.Unmarshal([]byte(state.RenderedConfig), &policy); err != nil {
		return err
	}
	// Shared-host paths were expanded before launch. Older local ~ rules use
	// the provider home persisted at creation, not the restarting wing's state.
	if meta, err := os.ReadFile(filepath.Join(dir, "egg.meta")); err == nil {
		for _, line := range strings.Split(string(meta), "\n") {
			if value, ok := strings.CutPrefix(line, "provider_home="); ok && value != "" {
				home = value
			}
		}
	}
	for _, denied := range policy.ToSandboxConfig(home).Deny {
		// The old Linux jail treats deny:/ as an allowlist switch, then
		// mounts HOME implicitly. It is not a recursive HOME deny.
		if denied != "/" && controlPathWithin(target, config.CanonicalProviderPath(denied)) {
			return nil
		}
	}
	return fmt.Errorf("its policy permits reading the controller directory")
}
