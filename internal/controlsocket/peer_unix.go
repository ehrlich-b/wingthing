//go:build darwin || linux

package controlsocket

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func ownedByUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}
func staleSocketError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist)
}
func verifyPeerUID(uid uint32) error {
	if uid != uint32(os.Getuid()) {
		return fmt.Errorf("local control peer UID %d differs from wing UID %d", uid, os.Getuid())
	}
	return nil
}
