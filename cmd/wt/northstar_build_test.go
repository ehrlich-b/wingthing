package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNorthstarBuildHelper(t *testing.T) {
	mode := os.Getenv("WT_NORTHSTAR_BUILD_HELPER")
	if mode == "" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Printf("%s %d\n", mode, os.Getpid())
	if mode == "root" {
		child := exec.Command(os.Args[0], "-test.run=^TestNorthstarBuildHelper$")
		child.Env = append(os.Environ(), "WT_NORTHSTAR_BUILD_HELPER=compiler")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
		// Own and reap the compiler child. Both ignore TERM, and the compiler
		// inherits the output pipe that would keep CombinedOutput blocked.
		_ = child.Wait()
	} else {
		wake := make(chan os.Signal, 1)
		signal.Notify(wake, syscall.SIGUSR1)
		<-wake
	}
	os.Exit(0)
}

func TestNorthstarBuildCancellationKillsCompilerGroup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestNorthstarBuildHelper$")
	cmd.Env = append(os.Environ(), "WT_NORTHSTAR_BUILD_HELPER=root")
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := northstarBuildOutput(ctx, cmd)
		done <- result{output, err}
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	})
	select {
	case got := <-done:
		if !errors.Is(got.err, context.DeadlineExceeded) {
			t.Fatalf("build cancellation: %v\n%s", got.err, got.output)
		}
		if !strings.Contains(string(got.output), "root ") || !strings.Contains(string(got.output), "compiler ") {
			t.Fatalf("build did not start both TERM-ignoring processes: %s", got.output)
		}
		if cmd.ProcessState == nil {
			t.Fatal("build returned before reaping the Go child")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("build cancellation hung")
	}
}
