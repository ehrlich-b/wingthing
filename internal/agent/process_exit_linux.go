package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

func processGroupSignalGone(_ *os.Process, err error) bool { return processSignalGone(err) }

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

func waitProcessExit(process *os.Process) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
	}
}

func processStopped(process *os.Process) (stopped, exited bool, err error) {
	var info unix.Siginfo
	for {
		err = unix.Waitid(unix.P_PID, process.Pid, &info, unix.WEXITED|unix.WSTOPPED|unix.WNOHANG|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			// CLD_STOPPED is 5; WNOWAIT preserves the eventual exit for Wait.
			return info.Signo != 0 && info.Code == 5, info.Signo != 0 && info.Code != 5, err
		}
	}
}
