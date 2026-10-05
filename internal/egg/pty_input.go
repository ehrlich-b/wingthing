package egg

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Darwin PTYs do not support os.File deadlines reliably. Use nonblocking
// syscalls and bounded poll waits so cancellation never leaves a write behind.
func preparePTYInput(file *os.File) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var setupErr error
	err = raw.Control(func(fd uintptr) { setupErr = unix.SetNonblock(int(fd), true) })
	return errors.Join(err, setupErr)
}

func writePTYInput(ctx context.Context, file *os.File, data []byte) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	for len(data) > 0 {
		if err = ctx.Err(); err != nil {
			return err
		}
		var n int
		var writeErr error
		err = raw.Control(func(fd uintptr) { n, writeErr = unix.Write(int(fd), data) })
		if err != nil {
			return err
		}
		if n > 0 {
			data = data[n:]
		}
		if writeErr == unix.EINTR {
			continue
		}
		if writeErr == unix.EAGAIN {
			if err = waitPTYReady(ctx, file, unix.POLLOUT); err != nil {
				return err
			}
		} else if writeErr != nil {
			return writeErr
		}
	}
	return nil
}

func waitPTYReady(ctx context.Context, file *os.File, events int16) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		var n int
		var pollErr error
		err = raw.Control(func(fd uintptr) {
			n, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: events}}, 50)
		})
		if err != nil {
			return err
		}
		if pollErr == unix.EINTR {
			continue
		}
		if pollErr != nil || n > 0 {
			return pollErr
		}
	}
}
