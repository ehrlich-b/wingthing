package egg

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

const containmentLaunchArg = "_egg_containment_exec"

// Re-exec a trusted launcher that cannot fork the provider until the egg has
// attached it to its cgroup and recorded its identity. Keep existing inherited
// descriptors and SysProcAttr (including sandbox namespaces) intact.
func containmentLaunch(cmd *exec.Cmd) (release func() error, closeGate func(), err error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, read)
	cmd.Args = append([]string{exe, containmentLaunchArg, strconv.Itoa(fd), cmd.Path}, cmd.Args[1:]...)
	cmd.Path = exe
	return func() error {
		_, err := write.Write([]byte{1})
		return errors.Join(err, write.Close(), read.Close())
	}, func() { _ = write.Close(); _ = read.Close() }, nil
}

func init() {
	if len(os.Args) < 5 || os.Args[1] != containmentLaunchArg {
		return
	}
	fd, err := strconv.Atoi(os.Args[2])
	if err != nil || fd < 3 {
		os.Exit(125)
	}
	gate := os.NewFile(uintptr(fd), "containment-gate")
	var ready [1]byte
	_, err = io.ReadFull(gate, ready[:])
	_ = gate.Close()
	if err != nil || ready[0] != 1 {
		os.Exit(125)
	}
	if err := syscall.Exec(os.Args[3], os.Args[3:], os.Environ()); err != nil {
		// Do not expose provider arguments or environment on a failed exec.
		os.Stderr.WriteString("egg: contained provider exec failed\n")
		os.Exit(126)
	}
}
