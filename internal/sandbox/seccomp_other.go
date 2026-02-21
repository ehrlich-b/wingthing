//go:build linux && !amd64

package sandbox

// No arch-specific denied syscalls on non-x86 platforms.
var deniedSyscallsArch []uint32
