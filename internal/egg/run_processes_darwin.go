package egg

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func sessionProcessGroupAlive(_ context.Context, pgid int) (bool, error) {
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return false, fmt.Errorf("inspect session process group: %w", err)
	}
	return sessionGroupHasLiveMembers(members), nil
}

const (
	processStateZombie = 5      // SZOMB in sys/proc.h
	processFlagExiting = 0x2000 // P_WEXIT in sys/proc.h
)

func processSnapshotContext(ctx context.Context) (map[int]observedProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// ps's state column loses P_WEXIT and can label exiting processes SRUN.
	// Use the same kernel exit states as the group termination check, retaining
	// only identity fields and the start time needed to reject PID reuse.
	members, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("process descendant inventory unavailable: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return darwinProcessSnapshot(members), nil
}

func darwinProcessSnapshot(members []unix.KinfoProc) map[int]observedProcess {
	out := make(map[int]observedProcess, len(members))
	for _, member := range members {
		if !sessionProcessLive(member.Proc) {
			continue
		}
		pid := int(member.Proc.P_pid)
		out[pid] = observedProcess{
			RunDescendant: RunDescendant{pid, int(member.Eproc.Ppid), int(member.Eproc.Pgid)},
			start:         fmt.Sprintf("%d.%06d", member.Proc.P_starttime.Sec, member.Proc.P_starttime.Usec),
		}
	}
	return out
}

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
		if sessionProcessLive(member.Proc) {
			return true
		}
	}
	return false
}

func sessionProcessLive(process unix.ExternProc) bool {
	return process.P_stat != processStateZombie && process.P_flag&processFlagExiting == 0
}
