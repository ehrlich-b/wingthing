package permissions

import (
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func sendHookInput(c net.Conn, input *os.File) error {
	conn, ok := c.(*net.UnixConn)
	if !ok {
		return errors.New("Unix hook channel required")
	}
	_, _, err := conn.WriteMsgUnix([]byte{1}, unix.UnixRights(int(input.Fd())), nil)
	return err
}
func receiveHookInput(c net.Conn) (*os.File, error) {
	conn, ok := c.(*net.UnixConn)
	if !ok {
		return nil, errors.New("Unix hook channel required")
	}
	data, oob := make([]byte, 1), make([]byte, unix.CmsgSpace(4))
	n, oobn, flags, _, err := conn.ReadMsgUnix(data, oob)
	if err != nil {
		return nil, err
	}
	messages, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return nil, err
	}
	var fds []int
	for _, message := range messages {
		rights, err := unix.ParseUnixRights(&message)
		if err == nil {
			fds = append(fds, rights...)
		}
	}
	if n != 1 || data[0] != 1 || flags&unix.MSG_CTRUNC != 0 || len(fds) != 1 {
		for _, fd := range fds {
			unix.Close(fd)
		}
		return nil, errors.New("one native hook input descriptor required")
	}
	unix.CloseOnExec(fds[0])
	return os.NewFile(uintptr(fds[0]), "provider-hook-input"), nil
}
