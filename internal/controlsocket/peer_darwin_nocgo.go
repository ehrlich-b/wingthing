//go:build darwin && !cgo

package controlsocket

import (
	"golang.org/x/sys/unix"
	"net"
)

func checkPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Xucred
	var peerErr error
	// getpeereid uses this same kernel credential query. Keep cross-built
	// Darwin binaries working without a libc dependency.
	if err := raw.Control(func(fd uintptr) {
		cred, peerErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return err
	}
	if peerErr != nil {
		return peerErr
	}
	return verifyPeerUID(cred.Uid)
}
