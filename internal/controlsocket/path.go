package controlsocket

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const socketPathFile = "control.socket-path"

// socketPath uses the shortest supported sockaddr_un limit (Darwin). The
// selected state directory retains all state; only an overlong IPC address
// moves to a private per-UID runtime directory. Canonical hashing makes state
// aliases select the same endpoint without trusting a caller-writable pointer.
func socketPath(dir string) (string, bool, error) {
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", false, err
	}
	path := filepath.Join(canonical, SocketName)
	if len(path) <= 103 {
		return path, false, nil
	}
	digest := sha256.Sum256([]byte(canonical))
	return filepath.Join(runtimeSocketDir(), fmt.Sprintf("%x.sock", digest)), true, nil
}

func runtimeSocketDir() string {
	// Do not use TMPDIR: its spelling can itself exceed sockaddr_un, and a
	// client and wing may have different temporary-directory environments.
	return fmt.Sprintf("/tmp/wt-%d", os.Getuid())
}

func verifyRuntimeSocketDir(dir string, create bool) error {
	if create {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || !ownedByUser(info) || info.Mode().Perm() != 0700 {
		return errors.New("local control runtime directory must be an owned, non-symlink directory with mode 0700")
	}
	return nil
}

func writeSocketPath(dir, path string) error {
	// Atomic publication in the wing's private state directory. This file is
	// for inspection; Dial derives and verifies the endpoint independently.
	file, err := os.CreateTemp(dir, ".control-socket-path-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(path + "\n"); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, socketPathFile))
}
