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

const gitDirectoryHelper = "_worktree_git_directory"

// macOS cannot traverse /dev/fd/N as a directory. A short-lived helper uses
// fchdir before exec instead; the host process's cwd is never changed. This
// also works in test binaries and inherits any existing caller sandbox.
func init() {
	if len(os.Args) < 5 || os.Args[1] != gitDirectoryHelper {
		return
	}
	if err := unix.Fchdir(3); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = unix.Close(3)
	cwd, err := os.Getwd()
	if err != nil || cwd != os.Args[2] {
		fmt.Fprintln(os.Stderr, "worktree directory changed before Git execution")
		os.Exit(1)
	}
	if err := unix.Exec(os.Args[3], append([]string{os.Args[3]}, os.Args[4:]...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Open each component relative to an already-open directory. No symlink,
// including a concurrently replaced ancestor, is followed during mutation.
func openDirectory(path string, create bool) (*os.File, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if component == "" {
			continue
		}
		next, err := unix.Openat(fd, component, flags, 0)
		if create && errors.Is(err, unix.ENOENT) {
			if err = unix.Mkdirat(fd, component, 0700); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(fd, component, flags, 0)
			}
		}
		_ = unix.Close(fd)
		if err != nil {
			return nil, fmt.Errorf("open worktree directory %q: %w", path, err)
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (m Manager) verifyDirectory(dir *os.File) error {
	path, err := m.admitPath(dir.Name())
	if err != nil {
		return err
	}
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	actual, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if path != dir.Name() || !os.SameFile(info, actual) {
		return fmt.Errorf("worktree path %q changed while creating", dir.Name())
	}
	return nil
}

func git(repo string, args ...string) (string, error) {
	dir, err := openDirectory(repo, false)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	return gitAt(dir, args...)
}

func gitAt(dir *os.File, args ...string) (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	argv := []string{gitDirectoryHelper, dir.Name(), gitPath,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "protocol.file.allow=never", "-c", "protocol.allow=never",
		"-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	cmd := exec.Command(exe, append(argv, args...)...)
	cmd.ExtraFiles = []*os.File{dir}
	// Allow only host executable lookup and fixed Git settings. In particular,
	// do not inherit Git selection/config overrides or loader injection vars.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1"}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(output), nil
}
