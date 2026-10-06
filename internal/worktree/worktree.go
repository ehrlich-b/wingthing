// Package worktree manages isolated Git checkouts within an admitted workspace.
package worktree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Manager requires explicit workspace bounds. Root, when set, replaces the
// default sibling .wingthing-worktrees directory; each repository gets a subdir.
type Manager struct {
	AllowedPaths []string
	Root         string
}

type Worktree struct {
	Name   string `json:"name"`
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
}

func ValidateName(name string) error {
	if name == "" {
		return errors.New("worktree name is required")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return errors.New("worktree names may contain only letters, digits, '-' and '_'")
		}
	}
	return nil
}

func (m Manager) Create(repo, name, base string) (*Worktree, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	repo, common, err := m.repository(repo)
	if err != nil {
		return nil, err
	}
	unlock, err := lockRepo(common)
	if err != nil {
		return nil, err
	}
	defer unlock()
	dir, err := m.directory(repo)
	if err != nil {
		return nil, err
	}
	path, err := m.admitPath(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("worktree path %q already exists", path)
	}
	if base == "" {
		base = "HEAD"
	}
	head, err := git(repo, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	branch := "wt/" + name
	if _, err := git(repo, "worktree", "add", "-b", branch, "--", path, strings.TrimSpace(head)); err != nil {
		return nil, err
	}
	return &Worktree{Name: name, Repo: repo, Path: path, Branch: branch, Head: strings.TrimSpace(head)}, nil
}

// List returns the repository's registered worktrees beneath this manager's root.
func (m Manager) List(repo string) ([]Worktree, error) {
	repo, common, err := m.repository(repo)
	if err != nil {
		return nil, err
	}
	unlock, err := lockRepo(common)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return m.list(repo)
}

// Remove deletes only a registered managed checkout, retaining its Git branch.
func (m Manager) Remove(repo, name string, force bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	repo, common, err := m.repository(repo)
	if err != nil {
		return err
	}
	unlock, err := lockRepo(common)
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := m.list(repo)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name != name {
			continue
		}
		_, actualCommon, err := m.repository(entry.Path)
		if err != nil {
			return err
		}
		if actualCommon != common {
			return errors.New("worktree no longer belongs to this repository")
		}
		if !force {
			status, err := git(entry.Path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
			if err != nil {
				return err
			}
			if status != "" {
				return fmt.Errorf("worktree %q has uncommitted changes; use --force to remove it", name)
			}
		}
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		_, err = git(repo, append(args, "--", entry.Path)...)
		return err
	}
	return fmt.Errorf("unknown worktree %q", name)
}

func (m Manager) repository(repo string) (string, string, error) {
	if repo == "" {
		repo = "."
	}
	path, err := m.admitPath(repo)
	if err != nil {
		return "", "", err
	}
	top, err := git(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	top, err = m.admitPath(strings.TrimSuffix(top, "\n"))
	if err != nil {
		return "", "", err
	}
	common, err := git(top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	common, err = m.admitPath(strings.TrimSuffix(common, "\n"))
	return top, common, err
}

func (m Manager) directory(repo string) (string, error) {
	root := m.Root
	if root == "" {
		root = filepath.Join(filepath.Dir(repo), ".wingthing-worktrees")
	}
	return m.admitPath(filepath.Join(root, filepath.Base(repo)))
}

func (m Manager) list(repo string) ([]Worktree, error) {
	dir, err := m.directory(repo)
	if err != nil {
		return nil, err
	}
	output, err := git(repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	entries := []Worktree{}
	var entry Worktree
	for _, field := range strings.Split(output, "\x00") {
		switch {
		case strings.HasPrefix(field, "worktree "):
			entry = Worktree{Repo: repo, Path: strings.TrimPrefix(field, "worktree ")}
		case strings.HasPrefix(field, "HEAD "):
			entry.Head = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch "):
			entry.Branch = strings.TrimPrefix(field, "branch refs/heads/")
		case field == "" && entry.Path != "":
			entry.Name = filepath.Base(entry.Path)
			if entry.Path != repo && filepath.Dir(entry.Path) == dir && ValidateName(entry.Name) == nil {
				canonical, err := m.admitPath(entry.Path)
				if err != nil {
					return nil, err
				}
				// A moved symlink must not turn removal into a different target.
				if canonical != entry.Path {
					return nil, fmt.Errorf("worktree path %q changed through a symlink", entry.Path)
				}
				entries = append(entries, entry)
			}
			entry = Worktree{}
		}
	}
	return entries, nil
}

func (m Manager) admitPath(path string) (string, error) {
	canonical, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	for _, allowed := range m.AllowedPaths {
		root, err := canonicalPath(allowed)
		if err != nil {
			return "", err
		}
		relative, err := filepath.Rel(root, canonical)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return canonical, nil
		}
	}
	return "", fmt.Errorf("worktree path %q is outside allowed workspace paths", canonical)
}

// Resolve existing ancestors as well as not-yet-created checkout directories.
func canonicalPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		if _, err := os.Lstat(path); err == nil {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		suffix = append(suffix, filepath.Base(path))
		path = filepath.Dir(path)
	}
}

func lockRepo(common string) (func(), error) {
	fd, err := unix.Open(filepath.Join(common, "wingthing-worktree.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "wingthing-worktree.lock")
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = file.Close() }, nil
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	// Repository selection must come from argv rather than ambient Git overrides.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(output), nil
}
