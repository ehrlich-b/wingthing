package egg

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
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

// Inspect each surviving legacy policy instead of imposing a global launch
// embargo. Custom policies may have removed the historical ~/.gnupg deny.
func RequireLegacySecretProtection(sessionDir string, toolCapability bool) error {
	home, _ := os.UserHomeDir()
	states := []string{filepath.Join(home, ".wingthing"), filepath.Join(home, ".wingthing-preview"), filepath.Dir(filepath.Dir(sessionDir))}
	if state, err := config.StateDir(); err == nil {
		states = append(states, state)
	}
	seen := make(map[string]bool)
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
			return fmt.Errorf("check legacy egg isolation: %w", err)
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
				return fmt.Errorf("inspect legacy egg PID: %w", err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || !procinfo.OwnedProcessIsAlive(pid) {
				continue
			}
			MarkLegacyEggForReplacement(dir)
			// Old macOS policies permit process-info on siblings, exposing
			// environment capabilities even when controller files are denied.
			if toolCapability && runtime.GOOS == "darwin" {
				return fmt.Errorf("legacy egg %s can inspect new tool capability environments; restart that session before enabling new tools; PTY sessions remain available", entry.Name())
			}
			if err := legacyDeniesControlDirectory(dir, controlDirectory(sessionDir), home); err != nil {
				return fmt.Errorf("legacy egg %s cannot exclude new controller secrets: %w; restart that session; its PTY remains available", entry.Name(), err)
			}
		}
	}
	return nil
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
