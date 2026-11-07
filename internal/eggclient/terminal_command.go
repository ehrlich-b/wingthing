package eggclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

type terminalCommand struct {
	Path      string
	Args, Env []string
	Dir       string
	WaitDelay time.Duration
}

// RunTerminalCommand keeps SSH and its terminal cleanup in a separate process.
// Losing the caller closes the lifetime pipe: the supervisor stops SSH before
// restoring the outer terminal, so an orphan cannot change its modes again.
func RunTerminalCommand(ctx context.Context, command *exec.Cmd) error {
	if command.Err != nil {
		return command.Err
	}
	input, inOK := command.Stdin.(*os.File)
	output, outOK := command.Stdout.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(output.Fd())) {
		return command.Run()
	}
	lifeRead, lifeWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer lifeRead.Close()
	defer lifeWrite.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	guardian := exec.Command(exe, terminalCommandArg)
	guardian.ExtraFiles = []*os.File{lifeRead, input, output, readyWrite}
	guardian.Stderr = command.Stderr
	guardian.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := guardian.Start(); err != nil {
		return err
	}
	readyWrite.Close()
	if err := json.NewEncoder(lifeWrite).Encode(terminalCommand{Path: command.Path, Args: command.Args, Env: command.Environ(), Dir: command.Dir, WaitDelay: command.WaitDelay}); err != nil {
		lifeWrite.Close()
		_ = guardian.Wait()
		return err
	}
	var snapshot terminalSnapshot
	if err := json.NewDecoder(readyRead).Decode(&snapshot); err != nil {
		lifeWrite.Close()
		_ = guardian.Wait()
		return fmt.Errorf("snapshot remote terminal: %w", err)
	}
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	done := make(chan error, 1)
	go func() { done <- guardian.Wait() }()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			lifeWrite.Close()
			<-done
			return ctx.Err()
		case <-winch:
			_ = guardian.Process.Signal(syscall.SIGWINCH)
		}
	}
}

func runGuardedTerminalCommand(life, input, output *os.File, pending []byte, known map[int]int) (int, error) {
	var request terminalCommand
	// A single decoder retains any read-ahead bytes for the lifetime EOF fence.
	decoder := json.NewDecoder(life)
	if err := decoder.Decode(&request); err != nil {
		return 0, err
	}
	master, slave, err := pty.Open()
	if err != nil {
		return 0, err
	}
	defer master.Close()
	defer slave.Close()
	if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
		return 0, err
	}
	resize := func() {
		if size, err := pty.GetsizeFull(input); err == nil {
			_ = pty.Setsize(master, size)
		}
	}
	resize()
	writer := newTerminalModeWriter(output, known)
	child := exec.Command(request.Path)
	child.Args, child.Env, child.Dir = request.Args, request.Env, request.Dir
	child.WaitDelay = request.WaitDelay
	child.Stdin, child.Stdout, child.Stderr = slave, writer, os.Stderr
	if term.IsTerminal(int(os.Stderr.Fd())) {
		child.Stderr = writer
	}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return 0, err
	}
	defer reader.Close()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	if err := child.Start(); err != nil {
		return 0, err
	}
	slave.Close()
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		_, _ = io.Copy(master, io.MultiReader(bytes.NewReader(pending), reader))
	}()
	go func() { defer workers.Done(); _, _ = io.Copy(writer, master) }()
	lost := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, io.MultiReader(decoder.Buffered(), life)); close(lost) }()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	var childErr error
	waiting := true
	for waiting {
		select {
		case <-lost:
			// Kill the SSH process group, including any inherited tty/pipe holders.
			_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			childErr = <-done
			waiting = false
		case childErr = <-done:
			waiting = false
		case <-winch:
			resize()
		}
	}
	reader.Cancel()
	master.Close()
	workers.Wait()
	var exit *exec.ExitError
	if errors.As(childErr, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, childErr
}
