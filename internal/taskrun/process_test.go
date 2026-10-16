package taskrun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestStopAgentProcessTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{name: "stopped"},
		{name: "no process group", err: syscall.ESRCH},
		{name: "already reaped", err: os.ErrProcessDone},
		{name: "permission denied", err: syscall.EPERM},
		{name: "wrapped permission denied", err: fmt.Errorf("kill: %w", syscall.EPERM)},
		{name: "unexpected failure", err: syscall.EIO, want: syscall.EIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			cmd := &exec.Cmd{Process: &os.Process{}, Cancel: func() error {
				called = true
				return tc.err
			}}
			err := stopAgentProcessTree(cmd)
			if !called {
				t.Fatal("process cancellation was not called")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("stopAgentProcessTree = %v, want %v", err, tc.want)
			}
			if tc.want != nil && !strings.Contains(err.Error(), "stop agent process tree:") {
				t.Fatalf("missing cleanup context: %v", err)
			}
		})
	}
}

func TestStopAgentProcessTreeWithoutCommand(t *testing.T) {
	for _, cmd := range []*exec.Cmd{nil, {}, {Process: &os.Process{}}} {
		if err := stopAgentProcessTree(cmd); err != nil {
			t.Fatalf("stopAgentProcessTree = %v", err)
		}
	}
}
