//go:build darwin && cgo

package controlsocket

/*
#include <sys/types.h>
#include <unistd.h>
*/
import "C"

import "net"

func checkPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var uid C.uid_t
	var gid C.gid_t
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		result, callErr := C.getpeereid(C.int(fd), &uid, &gid)
		if result != 0 {
			peerErr = callErr
		}
	}); err != nil {
		return err
	}
	if peerErr != nil {
		return peerErr
	}
	return verifyPeerUID(uint32(uid))
}
