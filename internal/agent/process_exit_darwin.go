package agent

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func processGroupSignalGone(process *os.Process, err error) bool {
	if processSignalGone(err) {
		return true
	}
	if !errors.Is(err, unix.EPERM) {
		return false
	}
	// Darwin refuses to signal zombies. EPERM is harmless only when a
	// census confirms there are no live members left in our pinned group.
	members, probeErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", process.Pid)
	if probeErr != nil {
		return false
	}
	for _, member := range members {
		if member.Proc.P_stat != 0 && member.Proc.P_stat != 5 {
			return false
		}
	}
	return true
}

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

func waitProcessExit(process *os.Process) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	unix.CloseOnExec(kq)
	change := unix.Kevent_t{Ident: uint64(process.Pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		if exited, probeErr := processExited(process); probeErr == nil && exited {
			return nil
		}
		return err
	}
	// Registration and this probe cover a provider that exits before we wait.
	if exited, err := processExited(process); err != nil || exited {
		return err
	}
	var events [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(kq, nil, events[:], nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 {
			if events[0].Flags&unix.EV_ERROR != 0 {
				return unix.Errno(events[0].Data)
			}
			return nil
		}
	}
}

func processStopped(process *os.Process) (stopped, exited bool, err error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", process.Pid)
	if err != nil {
		return false, false, err
	}
	if info.Proc.P_pid != int32(process.Pid) || info.Proc.P_stat == 0 {
		return false, false, os.ErrProcessDone
	}
	// SSTOP is 4 and SZOMB is 5 in Darwin's sys/proc.h.
	return info.Proc.P_stat == 4, info.Proc.P_stat == 5, nil
}
