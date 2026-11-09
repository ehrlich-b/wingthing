package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/testsocket"
)

func TestNorthstarBuildUsesProvidedBinaries(t *testing.T) {
	f := northstarNewFixture(t)
	for _, channel := range []string{"stable", "preview"} {
		source := filepath.Join(f.root, channel)
		northstarWrite(t, source, "#!/bin/sh\nprintf '%s\\n' '"+channel+" fixture'\n", 0700)
		key := "WT_TEST_BINARY"
		if channel == "preview" {
			key = "WT_TEST_PREVIEW_BINARY"
		}
		t.Setenv(key, source)
	}
	for _, channel := range []string{"stable", "preview"} {
		binary := northstarBuild(t, f, channel)
		if !strings.HasPrefix(binary, f.tmp+string(filepath.Separator)) {
			t.Fatalf("%s binary escaped fixture TMPDIR alias: %s", channel, binary)
		}
		if err := os.Remove(filepath.Join(f.root, channel)); err != nil {
			t.Fatal(err)
		}
		output, err := exec.CommandContext(t.Context(), binary).Output()
		if err != nil || string(output) != channel+" fixture\n" {
			t.Fatalf("%s fixture did not retain the selected binary: %v %s", channel, err, output)
		}
	}
}

func TestNorthstarBuildHelper(t *testing.T) {
	mode := os.Getenv("WT_NORTHSTAR_BUILD_HELPER")
	if mode == "" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	fmt.Printf("%s %d\n", mode, os.Getpid())
	if readyDir := os.Getenv("WT_NORTHSTAR_BUILD_READY_DIR"); readyDir != "" {
		if err := os.WriteFile(filepath.Join(readyDir, mode), nil, 0600); err != nil {
			panic(err)
		}
	}
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
	readyDir := testsocket.Dir(t)
	cmd.Env = append(os.Environ(), "WT_NORTHSTAR_BUILD_HELPER=root", "WT_NORTHSTAR_BUILD_READY_DIR="+readyDir)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
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
	// Cancellation must exercise the compiler group, rather than race its
	// startup on a busy host. Each TERM-ignoring process publishes readiness.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, rootErr := os.Stat(filepath.Join(readyDir, "root"))
		_, compilerErr := os.Stat(filepath.Join(readyDir, "compiler"))
		if rootErr == nil && compilerErr == nil {
			break
		}
		select {
		case got := <-done:
			t.Fatalf("build exited before both helpers were ready: %v\n%s", got.err, got.output)
		case <-ctx.Done():
			t.Fatal("build helpers did not become ready")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
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
