//go:build linux

package permissions

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// HookPeer authenticates the provider's exec-form hook channel. The credential
// is the kernel's peer PID together with the stdin channel created by the original
// provider. Nothing secret is inherited by Claude or its tool subprocesses.
// A tool running the hook executable itself still lacks this provider channel.
func HookPeer(root int, settings, binary string, c net.Conn, input *os.File) error {
	fail := errors.New("not a native provider hook channel")
	conn, ok := c.(*net.UnixConn)
	if !ok || root <= 0 {
		return fail
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	if err = raw.Control(func(fd uintptr) { cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || cred == nil {
		return fail
	}
	pid := int(cred.Pid)
	exe, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return fail
	}
	trusted, err := os.Stat(binary)
	if err != nil || !os.SameFile(exe, trusted) {
		return fail
	}
	parent, err := peerParent(pid)
	if err != nil {
		return fail
	}
	// Sandbox supervisors can precede the provider. Choose the outermost process
	// launched with this generated settings file, so a tool cannot start another
	// provider and claim its descendant hooks belong to the original process.
	provider := 0
	for ancestor, count := parent, 0; ancestor > 0 && count < 64; count++ {
		argv, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", ancestor))
		if readErr != nil {
			return fail
		}
		parts := bytes.Split(argv, []byte{0})
		for i := 0; i+1 < len(parts); i++ {
			// Supervisors forward provider argv only after their -- separator.
			if string(parts[i]) == "--" {
				break
			}
			if string(parts[i]) == "--settings" && string(parts[i+1]) == settings {
				provider = ancestor
			}
		}
		if ancestor == root {
			if provider != parent {
				return fail
			}
			if !nativeInput(input, pid, parent) {
				return fail
			}
			return nil
		}
		ancestor, err = peerParent(ancestor)
		if err != nil {
			return fail
		}
	}
	return fail
}
func peerParent(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 2 {
		return 0, errors.New("invalid process stat")
	}
	return strconv.Atoi(fields[1])
}
func providerPipe(hook, fd, provider, access int) bool {
	channel, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", hook, fd))
	if err != nil || !strings.HasPrefix(channel, "pipe:[") {
		return false
	}
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", provider))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := filepath.Join(fmt.Sprintf("/proc/%d", provider), "fd", entry.Name())
		target, err := os.Readlink(name)
		if err != nil || target != channel {
			continue
		}
		info, err := os.ReadFile(filepath.Join(fmt.Sprintf("/proc/%d", provider), "fdinfo", entry.Name()))
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(info), "\n") {
			if strings.HasPrefix(line, "flags:") {
				flags, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "flags:")), 8, 64)
				if err == nil && int(flags)&unix.O_ACCMODE == access {
					return true
				}
			}
		}
	}
	return false
}

func nativeInput(input *os.File, hook, provider int) bool {
	// Node's "pipe" stdio uses socketpair. SO_PEERCRED names the creator of
	// this specific channel, even after a descriptor crosses SCM_RIGHTS.
	cred, err := unix.GetsockoptUcred(int(input.Fd()), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err == nil {
		return int(cred.Pid) == provider
	}
	// Pipe-based fixture providers retain the writable end in their FD table.
	info, err := input.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return false
	}
	peerInfo, err := os.Stat(fmt.Sprintf("/proc/%d/fd/0", hook))
	return err == nil && os.SameFile(info, peerInfo) && providerPipe(hook, 0, provider, unix.O_WRONLY)
}

func NativeHookSupported() bool { return true }

func checkHookServer(c net.Conn, expected int) error {
	conn, ok := c.(*net.UnixConn)
	if !ok || expected < 0 {
		return errors.New("broker process binding required")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	if err = raw.Control(func(fd uintptr) { cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || cred == nil || int(cred.Pid) != expected {
		return errors.New("permission broker process changed")
	}
	return nil
}
