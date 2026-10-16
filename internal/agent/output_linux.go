//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"strconv"
)

// stopOutputWriters also finds detached or reparented descendants, which no
// longer belong to the provider process group. Only holders of this private
// pipe are eligible; unrelated processes are never selected by name or ancestry.
func stopOutputWriters(reader *os.File) {
	pipe, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(reader.Fd()), 10))
	if err != nil {
		return
	}
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		// FindProcess pins the process with a pidfd on supported Linux kernels,
		// avoiding a signal to a recycled PID while its descriptors are checked.
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", entry.Name(), "fd")
		fds, _ := os.ReadDir(dir)
		for _, fd := range fds {
			if target, err := os.Readlink(filepath.Join(dir, fd.Name())); err == nil && target == pipe {
				_ = process.Kill()
				break
			}
		}
		_ = process.Release()
	}
}
