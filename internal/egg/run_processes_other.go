//go:build !linux && !darwin

package egg

import (
	"errors"
	"os/exec"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/sandbox"
)

func processSnapshot() (map[int]observedProcess, error) {
	return nil, errors.New("process descendant inventory unavailable on this platform")
}
func signalIdentifiedProcess(observedProcess, syscall.Signal) error {
	return errors.New("process identity checks unavailable on this platform")
}
func reapDescendant(observedProcess, int) {}
func prepareProcessTree(*exec.Cmd, sandbox.Sandbox) (*runProcessTree, error) {
	return &runProcessTree{}, nil
}
