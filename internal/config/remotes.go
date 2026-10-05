package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/ehrlich-b/wingthing/internal/fsutil"
	"gopkg.in/yaml.v3"
)

type Remote struct {
	SSHTarget    string `yaml:"ssh_target" json:"ssh_target"`
	WingthingDir string `yaml:"wingthing_dir,omitempty" json:"wingthing_dir,omitempty"`
}

type remotesFile struct {
	Remotes map[string]Remote `yaml:"remotes"`
}

func ValidateRemoteName(name string) error {
	if name == "" {
		return errors.New("remote name must not be empty")
	}
	if strings.EqualFold(name, "local") {
		return errors.New("remote name 'local' is reserved for this machine")
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return fmt.Errorf("invalid remote name %q; use letters, digits, '-' and '_'", name)
		}
	}
	return nil
}

func ValidateSSHTarget(target string) error {
	if target == "" {
		return errors.New("missing SSH target")
	}
	if strings.HasPrefix(target, "-") {
		return errors.New("SSH target must not start with '-'")
	}
	for _, r := range target {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("SSH target must not contain whitespace or control characters")
		}
	}
	return nil
}

func validateRemotes(remotes map[string]Remote) error {
	for name, remote := range remotes {
		if err := ValidateRemoteName(name); err != nil {
			return err
		}
		if err := ValidateSSHTarget(remote.SSHTarget); err != nil {
			return fmt.Errorf("remote %q: %w", name, err)
		}
		if remote.WingthingDir != "" && (!strings.HasPrefix(remote.WingthingDir, "/") || strings.ContainsAny(remote.WingthingDir, "\x00\r\n")) {
			return fmt.Errorf("remote %q: wingthing_dir must be an absolute remote path without NUL, CR or LF", name)
		}
	}
	return nil
}

// LoadRemotes is read-only, including when no registry has been created yet.
func LoadRemotes(dir string) (map[string]Remote, error) {
	data, err := os.ReadFile(filepath.Join(dir, "remotes.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Remote{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read remotes.yaml: %w", err)
	}
	var file remotesFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil && err != io.EOF {
		return nil, fmt.Errorf("parse remotes.yaml: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("remotes.yaml must contain one YAML document")
	}
	if file.Remotes == nil {
		file.Remotes = map[string]Remote{}
	}
	if err := validateRemotes(file.Remotes); err != nil {
		return nil, fmt.Errorf("remotes.yaml: %w", err)
	}
	return file.Remotes, nil
}

// SaveRemotes replaces the registry atomically with an owner-only, synced file.
func SaveRemotes(dir string, remotes map[string]Remote) error {
	if err := validateRemotes(remotes); err != nil {
		return err
	}
	data, err := yaml.Marshal(remotesFile{Remotes: remotes})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".remotes.yaml-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, "remotes.yaml")); err != nil {
		return err
	}
	return fsutil.SyncDirectory(dir)
}

// UpdateRemotes holds a process-wide filesystem lock through read/modify/write,
// so concurrent add/rm commands cannot overwrite each other's changes.
func UpdateRemotes(dir string, update func(map[string]Remote) error) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".remotes.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	remotes, err := LoadRemotes(dir)
	if err != nil {
		return err
	}
	if err := update(remotes); err != nil {
		return err
	}
	return SaveRemotes(dir, remotes)
}
