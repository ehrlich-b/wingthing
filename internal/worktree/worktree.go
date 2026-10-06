// Package worktree manages isolated Git checkouts within an admitted workspace.
package worktree

import (
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"os"
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
	Name             string `json:"name"`
	Repo             string `json:"repo"`
	Path             string `json:"path"`
	Branch           string `json:"branch"`
	Head             string `json:"head"`
	CheckoutRequired bool   `json:"checkout_required,omitempty"`
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
	if strings.HasPrefix(base, "-") {
		return nil, errors.New("worktree base must not start with '-'")
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
	if base == "" {
		base = "HEAD"
	}
	head, err := git(repo, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return nil, err
	}
	parent, err := openDirectory(dir, true)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("worktree path %q already exists", path)
		}
		return nil, fmt.Errorf("create worktree path %q: %w", path, err)
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open worktree path %q: %w", path, err)
	}
	checkout := os.NewFile(uintptr(fd), path)
	defer checkout.Close()
	if err := m.verifyDirectory(checkout); err != nil {
		return nil, err
	}
	branch := "wt/" + name
	// The manager has no caller sandbox policy. Do not populate files on the
	// host: checkout (including hooks, LFS and filters) belongs in the child's
	// sandbox. Git writes through the inherited checkout cwd, never its mutable
	// absolute path, even if an ancestor is replaced after verification.
	if _, err := gitAt(checkout, "--git-dir="+common, "--work-tree=.", "worktree", "add", "--no-checkout", "-b", branch, "--", ".", strings.TrimSpace(head)); err != nil {
		// Only remove an empty leaf, through the pinned parent, on failure.
		if m.verifyDirectory(checkout) == nil {
			_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
		}
		return nil, err
	}
	if err := m.verifyDirectory(checkout); err != nil {
		return nil, err
	}
	return &Worktree{Name: name, Repo: repo, Path: path, Branch: branch, Head: strings.TrimSpace(head), CheckoutRequired: true}, nil
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
			dirty, err := dirtyWorktree(entry.Path)
			if err != nil {
				return err
			}
			if dirty {
				return fmt.Errorf("worktree %q has uncommitted changes; use --force to remove it", name)
			}
		}
		// Git's own non-forced removal runs status, which can execute clean
		// filters. The filter-free safety check above replaces that check.
		_, err = git(repo, "worktree", "remove", "--force", "--", entry.Path)
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
	dir, err := openDirectory(common, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Openat(int(dir.Fd()), "wingthing-worktree.lock", flags|unix.O_CREAT|unix.O_EXCL, 0600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(dir.Fd()), "wingthing-worktree.lock", flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open repository lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wingthing-worktree.lock")
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock repository: %w", err)
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = file.Close() }, nil
}

// dirtyWorktree deliberately compares raw blobs instead of running status:
// status may execute arbitrary clean/process filters, even without a checkout.
// Transformed files and submodules conservatively require force. ls-files
// without exclude rules includes ALL untracked files, including ignored .envs.
func dirtyWorktree(path string) (bool, error) {
	dir, err := openDirectory(path, false)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	root, err := os.OpenRoot(path)
	if err != nil {
		return false, err
	}
	defer root.Close()
	info, err := dir.Stat()
	if err != nil {
		return false, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		return false, fmt.Errorf("worktree path %q changed while opening", path)
	}
	other, err := gitAt(dir, "ls-files", "--others", "-z", "--")
	if err != nil || other != "" {
		return other != "", err
	}
	index, err := gitAt(dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return false, err
	}
	// --no-checkout leaves no index. An empty, unpopulated worktree can be
	// removed without treating its intentionally missing files as deletions.
	if _, err := os.Stat(strings.TrimSpace(index)); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	staged, err := gitAt(dir, "diff-index", "--cached", "--raw", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "HEAD", "--")
	if err != nil || staged != "" {
		return staged != "", err
	}
	tracked, err := gitAt(dir, "ls-files", "--stage", "-z", "--")
	if err != nil {
		return false, err
	}
	for _, entry := range strings.Split(strings.TrimSuffix(tracked, "\x00"), "\x00") {
		if entry == "" {
			continue
		}
		metadata, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 {
			return false, errors.New("invalid Git index entry")
		}
		if fields[2] != "0" || fields[0] == "160000" {
			return true, nil
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		} else if err != nil {
			return false, err
		}
		var content []byte
		if fields[0] == "120000" {
			if info.Mode()&os.ModeSymlink == 0 {
				return true, nil
			}
			target, err := root.Readlink(name)
			if err != nil {
				return false, err
			}
			content = []byte(target)
		} else {
			if !info.Mode().IsRegular() || (fields[0] == "100755") != (info.Mode()&0111 != 0) {
				return true, nil
			}
			content, err = root.ReadFile(name)
			if err != nil {
				return false, err
			}
		}
		var digest hash.Hash
		switch len(fields[1]) {
		case 40:
			digest = sha1.New()
		case 64:
			digest = sha256.New()
		default:
			return false, errors.New("invalid Git object ID")
		}
		_, _ = fmt.Fprintf(digest, "blob %d\x00", len(content))
		_, _ = digest.Write(content)
		if fmt.Sprintf("%x", digest.Sum(nil)) != fields[1] {
			return true, nil
		}
	}
	return false, nil
}
