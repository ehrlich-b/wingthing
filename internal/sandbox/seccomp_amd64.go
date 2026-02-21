//go:build linux && amd64

package sandbox

import "golang.org/x/sys/unix"

// x86-only syscalls not defined on arm64.
var deniedSyscallsArch = []uint32{
	unix.SYS_IOPL,
	unix.SYS_IOPERM,
	unix.SYS_MODIFY_LDT,
}
