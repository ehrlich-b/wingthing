package dashboard

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
	remotepkg "github.com/ehrlich-b/wingthing/internal/remote"
)

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("input failed") }

func fakeTerminal(output io.Writer, restored *atomic.Int32) *ansiTerminal {
	return &ansiTerminal{out: output, raw: func() (func() error, error) {
		return func() error { restored.Add(1); return nil }, nil
	}, getSize: func() (int, int) { return 80, 24 }}
}

func emptyInventory() *inventory {
	return newInventory(func(context.Context, string) ([]eggclient.LocalSession, error) { return nil, nil })
}

func TestDashboardTerminalRestoreOnEveryExit(t *testing.T) {
	failure := errors.New("test failure")
	for _, name := range []string{"quit", "ctrl-c", "eof", "input error", "input panic", "enter error", "render error", "panic", "canceled"} {
		t.Run(name, func(t *testing.T) {
			var restored atomic.Int32
			var writes atomic.Int32
			output := writerFunc(func(p []byte) (int, error) {
				count := writes.Add(1)
				if name == "enter error" || (name == "render error" && count >= 2) {
					return 0, failure
				}
				return len(p), nil
			})
			var input io.Reader = strings.NewReader("q")
			if name == "ctrl-c" {
				input = strings.NewReader("\x03")
			} else if name == "eof" {
				input = strings.NewReader("")
			} else if name == "input error" {
				input = errorReader{failure}
			} else if name == "input panic" {
				input = panicReader{}
			}
			terminal := fakeTerminal(output, &restored)
			if name == "panic" {
				terminal.getSize = func() (int, int) { panic("render panic") }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			var err error
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				err = run(ctx, &state{machines: map[string]machine{}}, emptyInventory(), nil, terminal, remotepkg.IO{In: input, Out: output}, nil, nil)
			}()
			if restored.Load() != 1 || terminal.active {
				t.Fatalf("terminal restoration count=%d active=%v", restored.Load(), terminal.active)
			}
			wantErr := name == "input error" || name == "enter error" || name == "render error"
			if errors.Is(err, failure) != wantErr || (recovered != nil) != (name == "panic") {
				t.Fatalf("err=%v panic=%v", err, recovered)
			}
			if name == "input panic" && (err == nil || !strings.Contains(err.Error(), "input failed")) {
				t.Fatal("input panic was not surfaced")
			}
		})
	}
}

func TestDashboardRawFailureAndRestoreRetry(t *testing.T) {
	failure := errors.New("raw failure")
	terminal := &ansiTerminal{out: io.Discard, raw: func() (func() error, error) { return nil, failure }}
	if err := terminal.enter(); !errors.Is(err, failure) || terminal.active {
		t.Fatal("raw failure entered the alternate screen")
	}
	var restores int
	terminal.raw = func() (func() error, error) {
		return func() error {
			restores++
			if restores == 1 {
				return failure
			}
			return nil
		}, nil
	}
	if err := terminal.enter(); err != nil {
		t.Fatal(err)
	}
	if err := terminal.leave(); !errors.Is(err, failure) || !terminal.active {
		t.Fatal("failed restore could not be retried")
	}
	if err := terminal.leave(); err != nil || terminal.active || restores != 2 {
		t.Fatal("deferred restore retry failed")
	}
}

func pipeInput(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = writer.Close() })
	return input, writer
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("fake-stream event timed out")
		var zero T
		return zero
	}
}

func TestDashboardAttachHandoffReturnsOnDetachOrError(t *testing.T) {
	for _, machineName := range []string{"local", "work"} {
		for _, fail := range []bool{false, true} {
			t.Run(machineName+"/"+map[bool]string{true: "error", false: "detach"}[fail], func(t *testing.T) {
				input, writer := pipeInput(t)
				var restored atomic.Int32
				entered := make(chan struct{}, 3)
				terminal := fakeTerminal(io.Discard, &restored)
				terminal.raw = func() (func() error, error) {
					entered <- struct{}{}
					return func() error { restored.Add(1); return nil }, nil
				}
				inv := newInventory(func(context.Context, string) ([]eggclient.LocalSession, error) {
					return []eggclient.LocalSession{session("one", "keep", "/src", "idle")}, nil
				})
				s := &state{machines: map[string]machine{machineName: {sessions: []eggclient.LocalSession{session("one", "keep", "/src", "idle")}}}, selected: machineName + ":one"}
				called := make(chan []string, 1)
				command := func(ctx context.Context, args []string, streams remotepkg.IO) error {
					if restored.Load() != 1 {
						return errors.New("attach received a raw dashboard terminal")
					}
					called <- args
					// The child owns stdin here. A lingering dashboard reader would
					// steal this byte and prevent the fake attachment from returning.
					buffer := make([]byte, 1)
					if _, err := io.ReadFull(streams.In, buffer); err != nil {
						return err
					}
					if buffer[0] != 'd' {
						return errors.New("dashboard consumed attachment input")
					}
					if fail {
						return errors.New("attachment failed")
					}
					return nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				finished := make(chan error, 1)
				go func() {
					finished <- run(ctx, s, inv, nil, terminal, remotepkg.IO{In: input, Out: io.Discard}, command, nil)
				}()
				receive(t, entered)
				if _, err := writer.Write([]byte{'\r'}); err != nil {
					t.Fatal(err)
				}
				args := receive(t, called)
				ref := "one"
				if machineName != "local" {
					ref = machineName + ":one"
				}
				if !reflect.DeepEqual(args, []string{"attach", "--", ref}) {
					t.Fatal(args)
				}
				if _, err := writer.Write([]byte{'d'}); err != nil {
					t.Fatal(err)
				}
				receive(t, entered)
				if _, err := writer.Write([]byte{'q'}); err != nil {
					t.Fatal(err)
				}
				if err := receive(t, finished); err != nil {
					t.Fatal(err)
				}
				if restored.Load() != 3 || (s.message == "attachment failed") != fail {
					t.Fatalf("restores=%d message=%s", restored.Load(), s.message)
				}
			})
		}
	}
}

func TestDashboardAttachRestoresOriginalStateAfterChildExit(t *testing.T) {
	for _, outcome := range []string{"detach", "sigterm cancellation", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			input, writer := pipeInput(t)
			var mode atomic.Int32 // 0: original cooked state, 1: dashboard, 2: child.
			var output bytes.Buffer
			entered := make(chan int32, 2)
			terminal := &ansiTerminal{out: &output, getSize: func() (int, int) { return 80, 24 }, raw: func() (func() error, error) {
				previous := mode.Swap(1)
				entered <- previous
				return func() error { mode.Store(previous); return nil }, nil
			}}
			called := make(chan struct{})
			exitChild := make(chan struct{})
			command := func(ctx context.Context, _ []string, _ remotepkg.IO) error {
				if mode.Load() != 0 {
					return errors.New("handoff did not restore cooked mode")
				}
				mode.Store(2)
				close(called)
				if outcome == "sigterm cancellation" {
					<-ctx.Done()
					return ctx.Err()
				}
				<-exitChild
				if outcome == "panic" {
					panic("child panic")
				}
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				err   error
				panic any
			}
			finished := make(chan result, 1)
			go func() {
				var got result
				defer func() { got.panic = recover(); finished <- got }()
				inv := newInventory(func(context.Context, string) ([]eggclient.LocalSession, error) { return nil, errors.New("offline") })
				got.err = run(ctx, fixture(), inv, nil, terminal, remotepkg.IO{In: input, Out: &output}, command, nil)
			}()
			if previous := receive(t, entered); previous != 0 {
				t.Fatal("initial state was not cooked")
			}
			if _, err := writer.Write([]byte{'\r'}); err != nil {
				t.Fatal(err)
			}
			receive(t, called)
			if outcome == "sigterm cancellation" {
				cancel()
			} else {
				close(exitChild)
			}
			if outcome == "detach" {
				if previous := receive(t, entered); previous != 0 {
					t.Fatalf("dashboard captured the child's terminal state: %d", previous)
				}
				if _, err := writer.Write([]byte{'q'}); err != nil {
					t.Fatal(err)
				}
			}
			got := receive(t, finished)
			if got.err != nil || (got.panic != nil) != (outcome == "panic") {
				t.Fatalf("err=%v panic=%v", got.err, got.panic)
			}
			if mode.Load() != 0 || terminal.active || !strings.HasSuffix(output.String(), leaveScreen) {
				t.Fatalf("child exit left mode=%d active=%v", mode.Load(), terminal.active)
			}
		})
	}
}

func TestDashboardSuspendRestoresAndResumeReenters(t *testing.T) {
	for _, trigger := range []string{"sigtstp", "ctrl-z", "ctrl-z prompt"} {
		t.Run(trigger, func(t *testing.T) {
			input, writer := pipeInput(t)
			var mode atomic.Int32
			var output bytes.Buffer
			entered := make(chan int32, 3)
			stopping := make(chan struct{})
			stopped := make(chan error, 1)
			terminal := &ansiTerminal{out: &output, getSize: func() (int, int) { return 80, 24 }, raw: func() (func() error, error) {
				previous := mode.Swap(1)
				entered <- previous
				return func() error { mode.Store(previous); return nil }, nil
			}}
			terminal.stop = func() error {
				if mode.Load() != 0 || terminal.active || !strings.HasSuffix(output.String(), leaveScreen) {
					return errors.New("suspended before restoring terminal, cursor, and screen")
				}
				close(stopping)
				// A suspended dashboard reader must not steal shell input.
				var buffer [1]byte
				_, err := io.ReadFull(input, buffer[:])
				if err == nil && buffer[0] != 's' {
					err = errors.New("dashboard consumed shell input")
				}
				stopped <- err
				return err
			}
			signals := make(chan os.Signal, 3)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			s := &state{machines: map[string]machine{}}
			if trigger == "ctrl-z prompt" {
				s.mode, s.input = rename, "keep"
			}
			go func() {
				finished <- run(ctx, s, emptyInventory(), nil, terminal, remotepkg.IO{In: input, Out: &output}, nil, signals)
			}()
			receive(t, entered)
			// A stray SIGCONT must not acquire another terminal state.
			signals <- syscall.SIGCONT
			if trigger == "sigtstp" {
				signals <- syscall.SIGTSTP
			} else if _, err := writer.Write([]byte{26}); err != nil {
				t.Fatal(err)
			}
			receive(t, stopping)
			if _, err := writer.Write([]byte{'s'}); err != nil {
				t.Fatal(err)
			}
			if err := receive(t, stopped); err != nil {
				t.Fatal(err)
			}
			signals <- syscall.SIGCONT
			if previous := receive(t, entered); previous != 0 {
				t.Fatalf("resume started in mode %d", previous)
			}
			if _, err := writer.Write([]byte{3}); err != nil {
				t.Fatal(err)
			}
			if err := receive(t, finished); err != nil {
				t.Fatal(err)
			}
			if mode.Load() != 0 || terminal.active || !strings.HasSuffix(output.String(), leaveScreen) {
				t.Fatal("resume lost final terminal cleanup")
			}
			if trigger == "ctrl-z prompt" && s.input != "keep" {
				t.Fatal("Ctrl-Z modified the prompt")
			}
		})
	}
}

func TestDashboardSuspensionCancellationAndErrorsRestoreTerminal(t *testing.T) {
	for _, outcome := range []string{"canceled", "stop failure", "resume failure"} {
		t.Run(outcome, func(t *testing.T) {
			input, _ := pipeInput(t)
			failure := errors.New("job control failure")
			var restored atomic.Int32
			terminal := fakeTerminal(io.Discard, &restored)
			var enters int
			terminal.raw = func() (func() error, error) {
				enters++
				if outcome == "resume failure" && enters == 2 {
					return nil, failure
				}
				return func() error { restored.Add(1); return nil }, nil
			}
			stopped := make(chan struct{})
			terminal.stop = func() error {
				close(stopped)
				if outcome == "stop failure" {
					return failure
				}
				return nil
			}
			signals := make(chan os.Signal, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				finished <- run(ctx, &state{machines: map[string]machine{}}, emptyInventory(), nil, terminal, remotepkg.IO{In: input, Out: io.Discard}, nil, signals)
			}()
			signals <- syscall.SIGTSTP
			receive(t, stopped)
			if outcome == "canceled" {
				cancel()
			} else if outcome == "resume failure" {
				signals <- syscall.SIGCONT
			}
			if err := receive(t, finished); errors.Is(err, failure) != (outcome != "canceled") {
				t.Fatal(err)
			}
			if terminal.active || restored.Load() < 1 || (outcome == "canceled" && enters != 1) {
				t.Fatal("suspension exit lost restoration or re-entered a canceled terminal")
			}
		})
	}
}

func TestDashboardResizeWithFakeStream(t *testing.T) {
	input, writer := pipeInput(t)
	frames := make(chan string, 16)
	output := writerFunc(func(p []byte) (int, error) { frames <- string(p); return len(p), nil })
	var restored atomic.Int32
	var dimensions atomic.Value
	dimensions.Store([2]int{80, 24})
	terminal := fakeTerminal(output, &restored)
	terminal.getSize = func() (int, int) { size := dimensions.Load().([2]int); return size[0], size[1] }
	resize := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- run(ctx, &state{machines: map[string]machine{}}, emptyInventory(), nil, terminal, remotepkg.IO{In: input, Out: output}, nil, resize)
	}()
	if got := receive(t, frames); got != enterScreen {
		t.Fatal("did not enter alternate screen")
	}
	renderedLines(t, receive(t, frames), 80, 24)
	dimensions.Store([2]int{40, 12})
	resize <- syscall.SIGWINCH
	renderedLines(t, receive(t, frames), 40, 12)
	if _, err := writer.Write([]byte{'q'}); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, finished); err != nil || restored.Load() != 1 {
		t.Fatalf("resize exit failed: %v", err)
	}
}

func TestDashboardStopRunsInBackground(t *testing.T) {
	input, writer := pipeInput(t)
	var restored atomic.Int32
	called := make(chan []string, 1)
	commandDone := make(chan struct{})
	command := func(ctx context.Context, args []string, streams remotepkg.IO) error {
		called <- args
		defer close(commandDone)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	s := fixture()
	inv := newInventory(func(context.Context, string) ([]eggclient.LocalSession, error) { return nil, errors.New("offline") })
	go func() {
		finished <- run(ctx, s, inv, map[string]config.Remote{}, fakeTerminal(io.Discard, &restored), remotepkg.IO{In: input, Out: io.Discard}, command, nil)
	}()
	if _, err := writer.Write([]byte("xy")); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, called); !reflect.DeepEqual(got, []string{"session", "kill", "--", "two"}) {
		t.Fatal(got)
	}
	if restored.Load() != 0 {
		t.Fatal("background stop released the terminal")
	}
	if _, err := writer.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := receive(t, finished); err != nil {
		t.Fatal(err)
	}
	receive(t, commandDone)
	if restored.Load() != 1 {
		t.Fatal("pending remote action prevented terminal cleanup")
	}
}

func TestDashboardQuitWaitsForBackgroundProcessCleanup(t *testing.T) {
	input, writer := pipeInput(t)
	var restored atomic.Int32
	called := make(chan struct{})
	canceled := make(chan struct{})
	reaped := make(chan struct{})
	command := func(ctx context.Context, _ []string, _ remotepkg.IO) error {
		close(called)
		<-ctx.Done()
		close(canceled)
		<-reaped
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer close(reaped)
	finished := make(chan error, 1)
	inv := newInventory(func(context.Context, string) ([]eggclient.LocalSession, error) { return nil, errors.New("offline") })
	go func() {
		finished <- run(ctx, fixture(), inv, nil, fakeTerminal(io.Discard, &restored), remotepkg.IO{In: input, Out: io.Discard}, command, nil)
	}()
	if _, err := writer.Write([]byte("xy")); err != nil {
		t.Fatal(err)
	}
	receive(t, called)
	if _, err := writer.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	receive(t, canceled)
	select {
	case <-finished:
		t.Fatal("dashboard exited before its background child was reaped")
	case <-time.After(20 * time.Millisecond):
	}
	// Unblock the fake wait without closing a channel twice in deferred cleanup.
	reaped <- struct{}{}
	if err := receive(t, finished); err != nil || restored.Load() != 1 {
		t.Fatalf("quit cleanup failed: %v, restores=%d", err, restored.Load())
	}
}
