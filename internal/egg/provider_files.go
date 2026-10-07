package egg

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Pin the trusted provider home before traversing its agent-writable contents.
func openProviderHome(home string) (*os.File, error) {
	fd, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open provider home", Path: home, Err: err}
	}
	return os.NewFile(uintptr(fd), home), nil
}

// Every component is opened relative to a pinned directory, never by reopening
// a discovered pathname. O_NOFOLLOW applies to directories as well as leaves
// on both macOS and Linux, including when a component is concurrently replaced.
func openProviderPath(root *os.File, path string, directory bool) (*os.File, error) {
	return openProviderPathMode(root, path, directory, false)
}

func openProviderPathMode(root *os.File, path string, directory, create bool) (*os.File, error) {
	if filepath.IsAbs(path) || path == "" {
		return nil, errors.New("provider path must be relative")
	}
	parts := strings.Split(path, string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsRune(part, '\x00') {
			return nil, errors.New("invalid provider path component")
		}
	}
	parent := root
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 || directory {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(parent.Fd()), part, flags, 0)
		if errors.Is(err, unix.ENOENT) && create && flags&unix.O_DIRECTORY != 0 {
			if mkdirErr := unix.Mkdirat(int(parent.Fd()), part, 0755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				err = mkdirErr
			} else {
				fd, err = unix.Openat(int(parent.Fd()), part, flags, 0)
			}
		}
		if parent != root {
			_ = parent.Close()
		}
		if err != nil {
			return nil, &os.PathError{Op: "open provider path", Path: path, Err: err}
		}
		parent = os.NewFile(uintptr(fd), filepath.Join(root.Name(), filepath.Join(parts[:i+1]...)))
	}
	info, err := parent.Stat()
	if err == nil && !directory && !info.Mode().IsRegular() {
		err = errors.New("provider session is not a regular file")
	}
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	return parent, nil
}

func recordedProviderSessionID(eggDir, agent string) (string, error) {
	root, err := openProviderHome(eggDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := openProviderPath(root, "egg.meta", false)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil // Legacy eggs can replay their journal without guessing IDs.
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRestoreChatMetadataBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxRestoreChatMetadataBytes {
		return "", errors.New("egg metadata is too large")
	}
	meta := ParseChatMeta(string(data))
	if meta["agent"] != "" && meta["agent"] != agent {
		return "", errors.New("provider agent does not match egg metadata")
	}
	id := meta["provider_session_id"]
	if id != "" && !validLifecycleID(id) {
		return "", errors.New("invalid recorded provider session ID")
	}
	return id, nil
}
