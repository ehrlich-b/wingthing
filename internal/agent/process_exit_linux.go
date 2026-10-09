package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

// Inspect exit without reaping: the provider must still pin the process group
// until completion cleanup sends its last signal. Unknown status fails closed.
func processExited(process *os.Process) (bool, error) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, process.Pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		return info.Signo != 0, err
	}
}
