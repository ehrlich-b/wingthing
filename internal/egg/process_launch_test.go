package egg

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestContainmentLaunchAllowsZeroProviderArguments(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	release, closeGate, err := containmentLaunch(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGate()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("zero-argument provider did not exec: %v", err)
	}
}

func TestContainmentLaunchClosedGateCannotExecuteProvider(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	cmd := exec.Command("/bin/sh", "-c", `printf executed > "$1"`, "fixture", marker)
	_, closeGate, err := containmentLaunch(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGate()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	closeGate() // attachment/startup failed: EOF must prevent exec entirely
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 125 {
		t.Fatalf("launcher did not fail closed: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider executed without containment: %v", err)
	}
}

func TestContainmentLaunchPreservesInheritedDescriptors(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "bridge-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("inherited namespace bridge"); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "cat <&3")
	cmd.ExtraFiles = []*os.File{file}
	release, closeGate, err := containmentLaunch(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGate()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "inherited namespace bridge" {
		t.Fatalf("provider lost inherited descriptor: %q", out.String())
	}
}
