//go:build darwin || linux

package protectedfile

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openPolicyResolved(path string) (*File, error) { return openPolicyPath(path, true) }

// Walk and inspect the same directory descriptors used to open the next entry.
// Both the alias chain and its target chain must be host-controlled. Resolving
// a pathname first would let a replaceable directory redirect a trusted file.
func openPolicyPath(path string, followLeaf bool) (*File, error) {
	absolute := path
	if !filepath.IsAbs(absolute) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		absolute = cwd + "/" + path // preserve .. until preceding links are walked
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fd, err := unix.Open("/", flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { unix.Close(fd) }()
	current := "/"
	pending := strings.Split(absolute, "/")
	links := 0
	for {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return nil, err
		}
		if (stat.Uid != 0 && int(stat.Uid) != os.Getuid()) || stat.Mode&0022 != 0 {
			return nil, &Error{path, "policy directory " + current + " must be owned by root or this OS account and not writable by group or others"}
		}
		for len(pending) > 0 && (pending[0] == "" || pending[0] == ".") {
			pending = pending[1:]
		}
		if len(pending) == 0 {
			return nil, &Error{path, "must be a regular file"}
		}
		part := pending[0]
		pending = pending[1:]
		next, err := unix.Openat(fd, part, flags, 0)
		if err == unix.ELOOP {
			if (!followLeaf && len(pending) == 0) || links >= 40 {
				return nil, &Error{path, "cannot follow policy link"}
			}
			links++
			buffer := make([]byte, 4096)
			n, err := unix.Readlinkat(fd, part, buffer)
			if err != nil || n == len(buffer) {
				return nil, &Error{path, "cannot read policy link"}
			}
			target := string(buffer[:n])
			if filepath.IsAbs(target) {
				root, err := unix.Open("/", flags|unix.O_DIRECTORY, 0)
				if err != nil {
					return nil, err
				}
				unix.Close(fd)
				fd, current = root, "/"
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		if err != nil {
			if err == unix.ENOENT || err == unix.ENOTDIR {
				return nil, &os.PathError{Op: "open policy", Path: path, Err: err}
			}
			return nil, &Error{path, "cannot open policy entry: " + err.Error()}
		}
		if err := unix.Fstat(next, &stat); err != nil {
			unix.Close(next)
			return nil, err
		}
		name := filepath.Join(current, part)
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			unix.Close(fd)
			fd, current = next, name
			continue
		}
		f := os.NewFile(uintptr(next), name)
		if len(pending) != 0 {
			f.Close()
			return nil, &Error{path, "non-directory policy component"}
		}
		info, dev, ino, err := inspect(f, true)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &File{File: f, Info: info, Dev: dev, Ino: ino, policy: true}, nil
	}
}
