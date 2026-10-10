//go:build darwin

package egg

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
)

func newTestCgroup() (processBoundary, error) { return nil, errors.New("cgroups require Linux") }

func TestIdentifiedSignalAcceptsAlreadyExitedProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	snapshot, err := processSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	old, present := snapshot[cmd.Process.Pid]
	if !present {
		t.Fatal("fixture process not inventoried")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := signalIdentifiedProcess(old, syscall.SIGKILL); err != nil {
		t.Fatalf("process disappearance reported as identity failure: %v", err)
	}
}
