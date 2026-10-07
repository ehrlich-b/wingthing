// Package protectedfile pins host-controlled files before inspecting or reading them.
package protectedfile

import (
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
}

// Error is a security refusal, rather than a missing configuration file.
type Error struct {
	Path, Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("protected file %s: %s", e.Path, e.Reason) }

// OpenResolved preserves existing dotfile aliases. The resolved destination is
// opened without following links and must match the original file's identity.
// Sandbox callers must separately seal replaceable alias directory entries.
func OpenResolved(path string) (*File, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	f, err := Open(resolved)
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
	if _, _, _, err := inspect(f.File); err != nil {
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
