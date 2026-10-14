//go:build !linux

package permissions

import (
	"errors"
	"net"
	"os"
)

// Without a verified provider hook channel, leave permission handling native.
func HookPeer(root int, settings, binary string, c net.Conn, input *os.File) error {
	return errors.New("native provider hook authentication is unavailable on this platform")
}

func NativeHookSupported() bool { return false }
func checkHookServer(c net.Conn, expected int) error {
	return errors.New("native hook channel authentication unavailable")
}
