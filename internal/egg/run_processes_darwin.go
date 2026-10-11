package egg

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	processStateZombie = 5      // SZOMB in sys/proc.h
	processFlagExiting = 0x2000 // P_WEXIT in sys/proc.h
)

func sessionGroupSignalError(pgid int, signalErr error) error {
	if !errors.Is(signalErr, syscall.EPERM) {
		return signalErr
	}
	// Darwin's killpg1 returns EPERM when no group member can be signalled.
	// Besides zombies, MCP children can be committed to kernel exit (P_LEXIT)
	// but still report SRUN after the provider's Wait completes. Sysctl exports
	// P_LEXIT as P_WEXIT before the process reaches SZOMB. Neither can resume
	// user code. Confirm there are no live members before accepting termination;
	// a genuine denied signal or an unavailable inventory must still fail.
	// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sig.c
	// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sysctl.c
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return errors.Join(signalErr, fmt.Errorf("inspect session process group: %w", err))
	}
	if sessionGroupHasLiveMembers(members) {
		return signalErr
	}
	return nil
}

func sessionGroupHasLiveMembers(members []unix.KinfoProc) bool {
	for _, member := range members {
		if member.Proc.P_stat != processStateZombie && member.Proc.P_flag&processFlagExiting == 0 {
			return true
		}
	}
	return false
}
