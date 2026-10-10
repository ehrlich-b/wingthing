//go:build !darwin && !linux

package controlsocket

import (
	"errors"
	"net"
	"os"
)

func ownedByUser(os.FileInfo) bool { return false }
func staleSocketError(error) bool  { return false }
func checkPeer(*net.UnixConn) error {
	return errors.New("local wing control is supported on Linux and macOS")
}
