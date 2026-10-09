package agent

import (
	"os"

	"golang.org/x/sys/unix"
)

func processExited(process *os.Process) (bool, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", process.Pid)
	if err != nil {
		return false, err
	}
	if info.Proc.P_pid != int32(process.Pid) || info.Proc.P_stat == 0 {
		return false, os.ErrProcessDone
	}
	// SZOMB is 5 in Darwin's sys/proc.h. Do not treat a zombie or an unknown
	// status as a provider that our subsequent group signal killed.
	return info.Proc.P_stat == 5, nil
}
