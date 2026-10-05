package egg

import (
	"fmt"
	"runtime"
	"syscall"
)

// SocketPathTooLongError reports the exact filesystem address that cannot fit
// in sockaddr_un. Lengths are bytes, including any multibyte path components.
type SocketPathTooLongError struct {
	Path     string
	Bytes    int
	MaxBytes int
}

func (e *SocketPathTooLongError) Error() string {
	return fmt.Sprintf("Unix socket path %q is %d bytes; %s allows at most %d bytes; set WINGTHING_DIR to a shorter state directory", e.Path, e.Bytes, runtime.GOOS, e.MaxBytes)
}

// ValidateSocketPath checks the address passed to net.Listen("unix", path)
// without changing files. Go's syscall SockaddrUnix reserves a terminating NUL
// for filesystem sockets: Darwin has 104 path bytes and Linux has 108.
func ValidateSocketPath(path string) error {
	maximum := len(syscall.RawSockaddrUnix{}.Path) - 1
	if len(path) > maximum {
		return &SocketPathTooLongError{Path: path, Bytes: len(path), MaxBytes: maximum}
	}
	return nil
}
