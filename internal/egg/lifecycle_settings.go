package egg

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"golang.org/x/sys/unix"
)

// Check the opened identity, never a separately reopened settings path. The
// caller passes the compiled session policy before adding the settings bridge.
func validateClaudeSettingsFile(f *os.File, policy *sandbox.Config) error {
	refuse := func() error { return errors.New("settings file is outside the session filesystem policy") }
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return refuse()
	}
	path, err := filepath.Abs(f.Name())
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return refuse()
	}
	info, err := os.Stat(resolved)
	opened, statErr := f.Stat()
	if err != nil || statErr != nil || !os.SameFile(info, opened) {
		return refuse()
	}
	if policy == nil {
		return nil
	} // explicit outer host boundary
	masks := make([]string, 0, len(policy.Deny))
	for _, mask := range policy.Deny {
		masks = append(masks, config.CanonicalProviderPath(mask))
	}
	jail := false
	for _, mask := range masks {
		if mask == "/" && runtime.GOOS == "linux" {
			jail = true
			continue
		}
		if settingsPathWithin(resolved, mask) || settingsPathWithin(path, mask) {
			return refuse()
		}
	}
	if !jail {
		return nil
	} // Seatbelt and non-jail Linux permit other host reads.
	// Rewalk the most-specific mount without following any directory links.
	// A lexical workspace match must not import an unmounted host inode.
	exposed, err := openJailedClaudeSettingsFile(path, policy.Mounts)
	if err != nil {
		return refuse()
	}
	defer exposed.Close()
	info, err = exposed.Stat()
	if err != nil || !os.SameFile(opened, info) {
		return refuse()
	}
	return nil
}

func openClaudeSettingsFile(path string, policy *sandbox.Config) (*os.File, error) {
	if policy != nil && runtime.GOOS == "linux" {
		for _, mask := range policy.Deny {
			if filepath.Clean(mask) == "/" {
				return openJailedClaudeSettingsFile(path, policy.Mounts)
			}
		}
	}
	if policy == nil {
		return openBoundRegularFile(path)
	}
	// macOS exposes these OS-owned aliases outside the provider namespace.
	// Resolve only the system prefix; provider-controlled descendants still
	// undergo the same no-follow walk as jailed paths.
	if runtime.GOOS == "darwin" {
		for _, prefix := range []string{"/tmp", "/var", "/etc"} {
			if settingsPathWithin(path, prefix) {
				resolved, err := filepath.EvalSymlinks(prefix)
				if err != nil {
					return nil, err
				}
				path = resolved + strings.TrimPrefix(path, prefix)
				break
			}
		}
	}
	return openJailedClaudeSettingsFile(path, []sandbox.Mount{{Source: "/"}})
}

// Pin the mounted source and each descendant with openat/O_NOFOLLOW. Walking
// the source's ancestors too prevents a replaced root from redirecting the read.
func openJailedClaudeSettingsFile(path string, mounts []sandbox.Mount) (*os.File, error) {
	refuse := func() (*os.File, error) {
		return nil, errors.New("settings file is outside the session filesystem policy")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	best := -1
	source := ""
	for _, mount := range mounts {
		target := mount.Target
		if target == "" {
			target = mount.Source
		}
		target = filepath.Clean(target)
		if !settingsPathWithin(path, target) || len(target) < best {
			continue
		}
		relative, err := filepath.Rel(target, path)
		if err != nil {
			return refuse()
		}
		best, source = len(target), filepath.Join(mount.Source, relative)
	}
	if best < 0 {
		return refuse()
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return refuse()
	}
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(strings.TrimPrefix(source, "/"), "/")
	for i, part := range parts {
		openFlags := flags
		if i < len(parts)-1 {
			openFlags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, part, openFlags, 0)
		if err != nil {
			return refuse()
		}
		_ = unix.Close(fd)
		fd = next
	}
	f := os.NewFile(uintptr(fd), path)
	fd = -1 // ownership of the final descriptor passes to the caller
	return f, nil
}

func settingsPathWithin(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
