//go:build linux

package main

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

func probeSeccomp() SeccompProbe {
	var p SeccompProbe

	// Try PTRACE_TRACEME
	_, _, err := unix.Syscall(unix.SYS_PTRACE, unix.PTRACE_TRACEME, 0, 0)
	p.PtraceBlocked = (err != 0)

	// Try mount — mount("none", "/tmp", "tmpfs", 0, "")
	source := []byte("none\x00")
	target := []byte("/tmp\x00")
	fstype := []byte("tmpfs\x00")
	_, _, err = unix.Syscall6(
		unix.SYS_MOUNT,
		uintptr(unsafe.Pointer(&source[0])),
		uintptr(unsafe.Pointer(&target[0])),
		uintptr(unsafe.Pointer(&fstype[0])),
		0, 0, 0,
	)
	p.MountBlocked = (err != 0)

	return p
}

func probeNamespace() NamespaceProbe {
	var p NamespaceProbe

	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return p
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			val := strings.TrimPrefix(line, "NSpid:")
			val = strings.TrimSpace(val)
			p.NSpid = val
			// Multiple tab-separated entries means we're in a PID namespace
			p.InPIDNamespace = strings.Contains(val, "\t")
			break
		}
	}

	return p
}
