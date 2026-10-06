package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

const (
	enterScreen = "\x1b[?1049h\x1b[?25l\x1b[?7l"
	leaveScreen = "\x1b[0m\x1b[?7h\x1b[?25h\x1b[?1049l"
)

type terminal interface {
	enter() error
	leave() error
	size() (int, int)
}

type ansiTerminal struct {
	out     io.Writer
	raw     func() (func() error, error)
	getSize func() (int, int)
	restore func() error
	active  bool
}

func fileTerminal(input, output *os.File) *ansiTerminal {
	fd := int(input.Fd())
	return &ansiTerminal{
		out: output,
		raw: func() (func() error, error) {
			previous, err := term.MakeRaw(fd)
			if err != nil {
				return nil, err
			}
			return func() error { return term.Restore(fd, previous) }, nil
		},
		getSize: func() (int, int) {
			if width, height, err := term.GetSize(int(output.Fd())); err == nil {
				return width, height
			}
			return 80, 24
		},
	}
}

func (t *ansiTerminal) enter() error {
	if t.active {
		return nil
	}
	restore, err := t.raw()
	if err != nil {
		return fmt.Errorf("put dashboard terminal in raw mode: %w", err)
	}
	t.restore, t.active = restore, true
	_, err = io.WriteString(t.out, enterScreen)
	return err
}

func (t *ansiTerminal) leave() error {
	if !t.active {
		return nil
	}
	// Restore termios even if stdout is broken; retain a failed restore so the
	// final deferred cleanup can retry it after a failed handoff.
	restoreErr := t.restore()
	if restoreErr == nil {
		t.active, t.restore = false, nil
	}
	_, writeErr := io.WriteString(t.out, leaveScreen)
	return errors.Join(writeErr, restoreErr)
}

func (t *ansiTerminal) size() (int, int) { return t.getSize() }

type inputEvent struct {
	data []byte
	err  error
}

type inputSession struct {
	events <-chan inputEvent
	stop   func() error
}

// cancelreader is already in go.mod. Its existing kqueue/epoll wakeup lets us
// join the reader before another process takes stdin, without polling a TTY.
func readInput(ctx context.Context, input io.Reader) (*inputSession, error) {
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	events := make(chan inputEvent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				select {
				case events <- inputEvent{err: fmt.Errorf("input panic: %v", recovered)}:
				case <-ctx.Done():
				}
			}
		}()
		buffer := make([]byte, 256)
		for {
			n, err := reader.Read(buffer)
			if n > 0 || err != nil {
				event := inputEvent{data: append([]byte(nil), buffer[:n]...), err: err}
				select {
				case events <- event:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var once sync.Once
	var closeErr error
	return &inputSession{events: events, stop: func() error {
		once.Do(func() {
			cancel()
			reader.Cancel()
			<-done
			closeErr = reader.Close()
		})
		return closeErr
	}}, nil
}
