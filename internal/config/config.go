package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var wingIDRegex = regexp.MustCompile(`^[0-9a-f]{20,32}$`)

// Kept as a variable so the unsupported-hard-link compatibility path can be
// exercised deterministically without requiring a special test filesystem.
var linkWingIDFile = os.Link

type Config struct {
	Dir               string            `yaml:"-"`
	DefaultAgent      string            `yaml:"default_agent"`
	DefaultEmbedder   string            `yaml:"default_embedder"`
	WingID            string            `yaml:"wing_id"`
	Hostname          string            `yaml:"-"` // os.Hostname(), not persisted
	PollInterval      string            `yaml:"poll_interval"`
	DefaultMaxRetries int               `yaml:"max_retries"`
	RoostURL          string            `yaml:"roost_url"`
	Vars              map[string]string `yaml:"vars"`
}

func Load() (*Config, error) {
	dir := os.Getenv("WINGTHING_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		dir = filepath.Join(home, ".wingthing")
	}
	if err := ensureStateDirectory(dir); err != nil {
		return nil, err
	}

	cfg := &Config{
		Dir:             dir,
		DefaultAgent:    "claude",
		DefaultEmbedder: "auto",
		PollInterval:    "1s",
		Vars:            make(map[string]string),
	}

	data, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			cfg.WingID, err = defaultWingID(dir)
			if err != nil {
				return nil, err
			}
			cfg.Hostname = defaultHostname()
			cfg.setStandardVars()
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.Dir = dir
	if cfg.Vars == nil {
		cfg.Vars = make(map[string]string)
	}
	if !wingIDRegex.MatchString(cfg.WingID) {
		cfg.WingID, err = defaultWingID(dir)
		if err != nil {
			return nil, err
		}
	}
	cfg.Hostname = defaultHostname()
	cfg.setStandardVars()
	return cfg, nil
}

// The state directory contains bearer tokens, session cookies, signing keys,
// terminal metadata, and SQLite databases. Individual secret files are also
// private, but protecting the directory prevents permissive umasks or legacy
// deployments from exposing database and sidecar files created by SQLite.
func ensureStateDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create wing config directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("protect wing config directory: %w", err)
	}
	return nil
}

func defaultWingID(dir string) (string, error) {
	idPath := filepath.Join(dir, "wing-id")
	if data, err := os.ReadFile(idPath); err == nil {
		id := strings.TrimSpace(string(data))
		if wingIDRegex.MatchString(id) {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read wing ID: %w", err)
	}
	id := strings.ReplaceAll(uuid.New().String(), "-", "")[:24]
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create wing config directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".wing-id-*")
	if err != nil {
		return "", fmt.Errorf("create temporary wing ID: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("protect temporary wing ID: %w", err)
	}
	if _, err := temporary.WriteString(id + "\n"); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write temporary wing ID: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("sync temporary wing ID: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close temporary wing ID: %w", err)
	}
	// Link provides an atomic create-if-absent commit. Concurrent first loads
	// must all return the same ID instead of each returning a value that another
	// process immediately overwrites.
	if err := linkWingIDFile(temporaryPath, idPath); err != nil {
		if !os.IsExist(err) && !hardLinksUnavailable(err) {
			return "", fmt.Errorf("install wing ID: %w", err)
		}
		// Some otherwise usable filesystems reject hard links. Serialize the
		// fallback (and corrupt-file repair) with an advisory lock, then re-read:
		// another first-load process may have installed its ID while we waited.
		lock, lockErr := acquireWingIDLock(dir)
		if lockErr != nil {
			return "", lockErr
		}
		defer func() { _ = lock.Close() }()
		if data, readErr := os.ReadFile(idPath); readErr == nil {
			winner := strings.TrimSpace(string(data))
			if wingIDRegex.MatchString(winner) {
				return winner, nil
			}
		} else if !os.IsNotExist(readErr) {
			return "", fmt.Errorf("re-read wing ID: %w", readErr)
		}
		// Rename is atomic and preserves the already-synced temporary file's
		// restrictive mode. The lock prevents concurrent fallback writers from
		// returning different IDs or racing a corrupt-file repair.
		if err := os.Rename(temporaryPath, idPath); err != nil {
			return "", fmt.Errorf("install wing ID without hard links: %w", err)
		}
	}
	if err := fsutil.SyncDirectory(dir); err != nil {
		return "", fmt.Errorf("persist wing ID: %w", err)
	}
	return id, nil
}

func hardLinksUnavailable(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EXDEV)
}

func acquireWingIDLock(dir string) (*os.File, error) {
	return acquireConfigLock(dir, ".wing-id.lock")
}

func acquireConfigLock(dir, name string) (*os.File, error) {
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open config lock %s: %w", name, err)
	}
	if err := lockConfigFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock config %s: %w", name, err)
	}
	return file, nil
}

func defaultHostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

func (c *Config) setStandardVars() {
	home, _ := os.UserHomeDir()
	c.Vars["HOME"] = home
	c.Vars["WINGTHING_DIR"] = c.Dir
	if cwd, err := os.Getwd(); err == nil {
		c.Vars["PROJECT_ROOT"] = cwd
	}
}

func (c *Config) ResolveVars(s string) string {
	for k, v := range c.Vars {
		s = strings.ReplaceAll(s, "$"+k, v)
	}
	return s
}

func (c *Config) DBPath() string {
	return filepath.Join(c.Dir, "wt.db")
}

func (c *Config) MemoryDir() string {
	return filepath.Join(c.Dir, "memory")
}

func (c *Config) SkillsDir() string {
	return filepath.Join(c.Dir, "skills")
}

func (c *Config) PromptsDir() string {
	return filepath.Join(c.Dir, "prompts")
}

func (c *Config) RelayDBPath() (string, error) {
	newPath := filepath.Join(c.Dir, "roost.db")
	oldPath := filepath.Join(c.Dir, "social.db")
	if info, err := os.Stat(newPath); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("relay database path %s is not a regular file", newPath)
		}
		return newPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect relay database: %w", err)
	}
	lock, err := acquireConfigLock(c.Dir, ".relay-db-migration.lock")
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close() }()
	// Another process may have completed the migration while this caller waited.
	if info, err := os.Stat(newPath); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("relay database path %s is not a regular file", newPath)
		}
		return newPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect relay database after lock: %w", err)
	}
	if info, err := os.Stat(oldPath); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("legacy relay database path %s is not a regular file", oldPath)
		}
		// Move sidecars first and the main DB last. The main rename is the commit
		// marker: after a crash, a remaining social.db causes the next start to
		// finish the migration, while roost.db means all existing sidecars were
		// already moved.
		movedSidecars := make([][2]string, 0, 2)
		for _, suffix := range []string{"-wal", "-shm"} {
			from, to := oldPath+suffix, newPath+suffix
			if renameErr := os.Rename(from, to); renameErr != nil {
				if os.IsNotExist(renameErr) {
					continue
				}
				rollbackRelaySidecars(movedSidecars)
				return "", fmt.Errorf("migrate relay database sidecar %s: %w", suffix, renameErr)
			}
			movedSidecars = append(movedSidecars, [2]string{from, to})
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			rollbackRelaySidecars(movedSidecars)
			return "", fmt.Errorf("migrate relay database: %w", err)
		}
		if err := fsutil.SyncDirectory(c.Dir); err != nil {
			return "", fmt.Errorf("persist relay database migration: %w", err)
		}
		return newPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect legacy relay database: %w", err)
	}
	return newPath, nil
}

func rollbackRelaySidecars(moved [][2]string) {
	for index := len(moved) - 1; index >= 0; index-- {
		_ = os.Rename(moved[index][1], moved[index][0])
	}
}
