// Package protectedfile pins host-controlled files before inspecting or reading them.
package protectedfile

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// File carries the identity of the descriptor whose contents callers consume.
type File struct {
	*os.File
	Info     os.FileInfo
	Dev, Ino uint64
	policy   bool
}

// Error is a security refusal, rather than a missing configuration file.
type Error struct {
	Path, Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("protected file %s: %s", e.Path, e.Reason) }

// ResolvedPath binds a mask's pathname to the identity already opened.
func (f *File) ResolvedPath() (string, error) {
	path, err := filepath.EvalSymlinks(f.Name())
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !os.SameFile(f.Info, info) {
		return "", &Error{f.Name(), "changed before resolving masks"}
	}
	return path, nil
}

// OpenResolved preserves existing dotfile aliases. The resolved destination is
// opened without following links and must match the original file's identity.
// Sandbox callers must separately seal replaceable alias directory entries.
func OpenResolved(path string) (*File, error) { return openResolved(path, false) }

func OpenPolicyResolved(path string) (*File, error) { return openPolicyResolved(path) }

func openResolved(path string, policy bool) (*File, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	f, err := open(resolved, policy)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, f.Info) {
		f.Close()
		return nil, &Error{path, "changed while being opened"}
	}
	return f, nil
}

// Read consumes only the validated descriptor, then rechecks it so a new hard
// link created during the read cannot silently pass validation.
func (f *File) ReadAll() ([]byte, error) {
	data, err := io.ReadAll(f.File)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := inspect(f.File, f.policy); err != nil {
		return nil, err
	}
	return data, nil
}

func ReadFile(path string) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadAll()
}

func ReadResolved(path string) ([]byte, error) {
	f, err := OpenResolved(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadAll()
}

// WriteFile stages secrets beneath eggs, a directory permanently masked by
// every current sandbox, then publishes a new inode. Adjacent named temporary
// files would escape masks for the final credential pathname.
func WriteFile(path string, data []byte) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	staging := filepath.Join("eggs", ".credential-staging")
	for _, name := range []string{"eggs", staging} {
		if err := root.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if err := inspectSecretDirectory(info); err != nil {
			return &Error{filepath.Join(root.Name(), name), err.Error()}
		}
	}
	temporary := filepath.Join(staging, "."+filepath.Base(path)+"-"+rand.Text())
	f, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(temporary, filepath.Base(path))
}

func ReadPolicyResolved(path string) ([]byte, error) {
	f, err := OpenPolicyResolved(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadAll()
}
