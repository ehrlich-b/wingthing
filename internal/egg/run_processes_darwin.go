//go:build darwin

package egg

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
	"golang.org/x/sys/unix"
)

func darwinProcess(info unix.KinfoProc) observedProcess {
	sid, _ := unix.Getsid(int(info.Proc.P_pid))
	return observedProcess{
		RunDescendant: RunDescendant{int(info.Proc.P_pid), int(info.Eproc.Ppid), int(info.Eproc.Pgid)},
		start:         fmt.Sprintf("%d.%06d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec),
		session:       sid, zombie: info.Proc.P_stat == 5,
	}
}

func processSnapshot() (map[int]observedProcess, error) {
	infos, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, errors.New("process descendant inventory unavailable")
	}
	out := make(map[int]observedProcess, len(infos))
	for _, info := range infos {
		p := darwinProcess(info)
		if p.PID > 0 {
			out[p.PID] = p
		}
	}
	return out, nil
}

func signalIdentifiedProcess(process observedProcess, signal syscall.Signal) error {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", process.PID)
	if errors.Is(err, unix.ESRCH) || (err == nil && int(info.Proc.P_pid) != process.PID) {
		return nil
	}
	if err != nil {
		return errors.New("process identity unavailable")
	}
	if darwinProcess(*info).start != process.start || process.zombie {
		return nil
	}
	err = syscall.Kill(process.PID, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func reapDescendant(observedProcess, int) {}

func prepareProcessTree(_ *exec.Cmd, _ sandbox.Sandbox) (*runProcessTree, error) {
	return &runProcessTree{}, nil
}
