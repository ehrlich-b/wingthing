package egg

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"golang.org/x/sys/unix"
)

// agentConfigFiles maps agent names to their critical config files (relative to $HOME).
var agentConfigFiles = map[string][]string{
	"claude": {"~/.claude/settings.json"},
	"codex":  {"~/.codex/config.json"},
	"cursor": {"~/.cursor/settings.json"},
}

// ConfigSnapshot holds copies of agent config files taken before a session.
type ConfigSnapshot struct {
	home   *os.File
	policy *sandbox.Config
	files  map[string][]byte // home-relative path -> original content (nil = didn't exist)
}

// SnapshotAgentConfig pins critical config files inside the provider home.
// A sandbox caller must supply its compiled policy before any host-side read.
func SnapshotAgentConfig(agent, isolatedHome string, policies ...*sandbox.Config) *ConfigSnapshot {
	if agent == "claude" && isolatedHome != "" {
		return nil
	}
	paths, ok := agentConfigFiles[agent]
	if !ok {
		return nil
	}
	home := isolatedHome
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil
		}
	}
	snap := &ConfigSnapshot{files: make(map[string][]byte)}
	if len(policies) > 0 {
		snap.policy = policies[0]
	}
	var err error
	snap.home, err = openProviderHome(home)
	if err != nil {
		return snap
	}
	for _, path := range paths {
		relative, err := filepath.Rel(home, expandTilde(path, home))
		if err != nil {
			continue
		}
		f, err := openProviderRegularFile(snap.home, relative)
		if errors.Is(err, os.ErrNotExist) {
			if snap.allowsPath(relative) {
				snap.files[relative] = nil
			}
			continue
		}
		if err != nil {
			continue
		} // refusals must never become a missing-file snapshot
		if snap.allowsPath(relative) && validateClaudeSettingsFile(f, snap.policy) == nil {
			if data, err := io.ReadAll(f); err == nil {
				// Recheck link count and policy after consuming the descriptor.
				if validateClaudeSettingsFile(f, snap.policy) == nil {
					snap.files[relative] = data
				}
			}
		}
		f.Close()
	}
	return snap
}

func (s *ConfigSnapshot) allowsPath(relative string) bool {
	if s.policy == nil {
		return true
	}
	path := filepath.Join(s.home.Name(), relative)
	jail := false
	for i, masks := range [][]string{s.policy.Deny, s.policy.ControlDenyPaths, s.policy.DenyWrite} {
		for _, mask := range masks {
			if mask == "/" && runtime.GOOS == "linux" && i == 0 {
				jail = true
				continue
			}
			if controlPathWithin(path, mask) {
				return false
			}
		}
	}
	if jail {
		best := -1
		exposed := ""
		for _, mount := range s.policy.Mounts {
			target := mount.Target
			if target == "" {
				target = mount.Source
			}
			if controlPathWithin(path, target) && len(target) > best {
				relative, err := filepath.Rel(target, path)
				if err != nil {
					return false
				}
				best, exposed = len(target), filepath.Join(mount.Source, relative)
			}
		}
		if best < 0 || exposed != path {
			return false
		}
	}
	return true
}

// Restore publishes fresh inodes through no-follow parent descriptors. It never
// follows a substituted leaf or parent, and releases the pinned home afterward.
func (s *ConfigSnapshot) Restore() {
	if s == nil || s.home == nil {
		return
	}
	defer s.Close()
	for path, data := range s.files {
		dir, err := openProviderDirectory(s.home, filepath.Dir(path), data != nil)
		if err != nil {
			log.Printf("egg: snapshot: open %s: %v", path, err)
			continue
		}
		err = s.restoreFile(dir, filepath.Base(path), data)
		dir.Close()
		if err != nil {
			log.Printf("egg: snapshot: restore %s: %v", path, err)
		}
	}
}

func (s *ConfigSnapshot) restoreFile(dir *os.File, name string, data []byte) error {
	current, err := openProviderLeaf(dir, name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if current != nil {
		if err := validateClaudeSettingsFile(current, s.policy); err != nil {
			current.Close()
			return err
		}
		content, readErr := io.ReadAll(current)
		current.Close()
		if readErr != nil {
			return readErr
		}
		if data != nil && bytes.Equal(content, data) {
			return nil
		}
	}
	if data == nil {
		if current == nil {
			return nil
		}
		return unix.Unlinkat(int(dir.Fd()), name, 0)
	}
	temporary := ".config-snapshot-" + rand.Text()
	fd, err := unix.Openat(int(dir.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), temporary))
	defer f.Close()
	defer unix.Unlinkat(int(dir.Fd()), temporary, 0)
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return unix.Renameat(int(dir.Fd()), temporary, int(dir.Fd()), name)
}

func (s *ConfigSnapshot) Close() {
	if s != nil && s.home != nil {
		s.home.Close()
		s.home = nil
	}
}
