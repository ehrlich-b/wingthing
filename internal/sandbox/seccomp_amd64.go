//go:build linux && amd64

package sandbox

import "golang.org/x/sys/unix"

// x86-only syscalls not defined on arm64.
var deniedSyscallsArch = []uint32{
	unix.SYS_IOPL,
	unix.SYS_IOPERM,
	unix.SYS_MODIFY_LDT,
}

// seccompNativeArch is AUDIT_ARCH_X86_64: the seccomp filter rejects any other
// architecture so the number-keyed denylist can't be bypassed via a 32-bit
// compat ABI. seccompX32Bit is __X32_SYSCALL_BIT — x32 shares AUDIT_ARCH_X86_64
// but sets this bit and uses different numbers, so it is rejected by range.
const (
	seccompNativeArch uint32 = 0xC000003E // AUDIT_ARCH_X86_64
	seccompX32Bit     uint32 = 0x40000000 // __X32_SYSCALL_BIT
)
