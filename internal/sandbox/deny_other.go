//go:build !linux

package sandbox

// DenyInit is only supported on Linux (macOS uses seatbelt deny rules instead).
func DenyInit(args []string) {
	panic("_deny_init is only supported on Linux")
}
