//go:build linux

package main

import (
	"os"
	"strconv"
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

// probeIsolation scans the procfs the agent can see. In a sealed jail this is
// a private procfs for a private PID namespace, so only the jail's own few
// processes are visible and no other process's environ is readable — in
// particular a secret planted in the roost/host process env (WT_TEST_HOST_SECRET,
// whose value the runner also passes to us as WT_TEST_FIND_SECRET) must NOT be
// findable in any /proc/<pid>/environ other than our own. If the host procfs
// leaked through, both the pid count would be large and the secret would appear.
func probeIsolation() IsolationProbe {
	var p IsolationProbe
	self := os.Getpid()
	needle := os.Getenv("WT_TEST_FIND_SECRET")

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return p
	}
	for _, e := range entries {
		pid, perr := strconv.Atoi(e.Name())
		if perr != nil {
			continue // not a /proc/<pid> entry
		}
		p.VisiblePids++
		if needle == "" || pid == self {
			continue
		}
		// A leaked host procfs lets us read other processes' environ.
		data, rerr := os.ReadFile("/proc/" + e.Name() + "/environ")
		if rerr != nil {
			continue // expected: not permitted / not our namespace
		}
		if strings.Contains(string(data), needle) {
			p.HostSecretVisible = true
		}
	}
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
