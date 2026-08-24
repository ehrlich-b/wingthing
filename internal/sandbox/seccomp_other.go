//go:build linux && !amd64

package sandbox

// No arch-specific denied syscalls on non-x86 platforms.
var deniedSyscallsArch []uint32

// seccompNativeArch == 0 disables the seccomp architecture check on platforms
// we don't specifically harden (the number-keyed denylist still applies, as
// before). amd64 — the production target — sets a real value in seccomp_amd64.go.
const (
	seccompNativeArch uint32 = 0
	seccompX32Bit     uint32 = 0
)
