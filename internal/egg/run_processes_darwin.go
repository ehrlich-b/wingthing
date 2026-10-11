package egg

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

const processStateZombie = 5 // SZOMB in sys/proc.h

func sessionGroupSignalError(pgid int, signalErr error) error {
	if !errors.Is(signalErr, syscall.EPERM) {
		return signalErr
	}
	// Darwin's killpg1 skips zombies and returns EPERM if no member can be
	// signalled. After the provider is reaped, exited MCP children can keep its
	// group alive until their parent reaps them. Confirm there are no live
	// members before treating this as successful termination; a genuine denied
	// signal or an unavailable inventory must still fail.
	// https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_sig.c
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return errors.Join(signalErr, fmt.Errorf("inspect session process group: %w", err))
	}
	for _, member := range members {
		if member.Proc.P_stat != processStateZombie {
			return signalErr
		}
	}
	return nil
}
