package procinfo

import (
	"os"
	"syscall"
)

// OwnedProcessIsAlive probes a process recorded by this wt instance. Every PID
// stored in the daemon, task, and egg metadata belongs to a same-UID child (the
// sandboxed agent may use another UID, but its supervising egg does not). EPERM
// therefore means that the PID has been recycled by a foreign process, not that
// the recorded child is still alive.
func OwnedProcessIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return ownedProcessSignalIndicatesAlive(proc.Signal(syscall.Signal(0)))
}

func ownedProcessSignalIndicatesAlive(err error) bool {
	return err == nil
}
