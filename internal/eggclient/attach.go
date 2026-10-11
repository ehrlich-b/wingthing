package eggclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"

	"github.com/ehrlich-b/wingthing/internal/ws"
	"github.com/muesli/cancelreader"

	"golang.org/x/term"
)

const attachPrefix byte = 0x02 // Ctrl+B

func ValidateSessionID(sessionID string) error {
	if !ws.ValidSessionID(sessionID) {
		return fmt.Errorf("invalid session ID %q", sessionID)
	}
	return nil
}

type AttachInputFilter struct {
	pendingPrefix bool
	paste         bool
	marker        int
}

// filter returns bytes for the egg and whether the local client should detach.
// The prefix may arrive in a separate read from the following key.
func (f *AttachInputFilter) Filter(input []byte) ([]byte, bool) {
	output := make([]byte, 0, len(input)+1)
	for _, b := range input {
		// Forward the markers themselves without buffering input. Only a
		// six-byte marker cursor is retained, even for an unterminated paste.
		marker := "\x1b[200~"
		if f.paste {
			marker = "\x1b[201~"
		}
		literal := f.paste
		if b == marker[f.marker] {
			f.marker++
			if f.marker == len(marker) {
				f.paste = !f.paste
				f.marker = 0
			}
		} else {
			f.marker = 0
			if b == marker[0] {
				f.marker = 1
			}
		}
		if literal {
			output = append(output, b)
			continue
		}
		if f.pendingPrefix {
			f.pendingPrefix = false
			switch b {
			case 'q', 'Q':
				return output, true
			case attachPrefix:
				output = append(output, attachPrefix)
			default:
				output = append(output, attachPrefix, b)
			}
			continue
		}
		if b == attachPrefix {
			f.pendingPrefix = true
			continue
		}
		output = append(output, b)
	}
	return output, false
}

// Flush preserves a final literal prefix when the input source closes.
func (f *AttachInputFilter) Flush() []byte {
	if f.pendingPrefix {
		f.pendingPrefix = false
		return []byte{attachPrefix}
	}
	return nil
}

// AttachConnectionError means no provider exit was confirmed. Callers can
// offer their exact route without confusing a broken transport with an exit.
type AttachConnectionError struct {
	SessionID string
	Err       error
}

func (e *AttachConnectionError) Error() string {
	return "session outcome unknown; the session may still be running: " + e.Err.Error()
}
func (e *AttachConnectionError) Unwrap() error { return e.Err }

type SessionExitError struct{ Code int }

func (e *SessionExitError) Error() string { return fmt.Sprintf("session exited with code %d", e.Code) }

type attachDiagnosticsKey struct{}

func WithAttachDiagnostics(ctx context.Context, out io.Writer) context.Context {
	return context.WithValue(ctx, attachDiagnosticsKey{}, out)
}

type serializedSessionStream struct {
	SessionStream
	mu sync.Mutex
}

func (s *serializedSessionStream) Send(msg *pb.SessionMsg) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SessionStream.Send(msg)
}

type attachResult struct {
	sessionID string
	exitCode  *int
	detached  bool
	err       error
}

func AttachLocal(ctx context.Context, cfg *config.Config, sessionID string) (bool, error) {
	return AttachLocalOptions(ctx, cfg, sessionID, egg.AttachOptions{Claim: true, Owner: "cli"})
}

func AttachLocalOptions(ctx context.Context, cfg *config.Config, sessionID string, options egg.AttachOptions) (bool, error) {
	return AttachLocalIO(ctx, cfg, sessionID, os.Stdin, os.Stdout, options)
}

// The attachment owns its stream lifetime, not the session process. Detaching
// or losing this connection cancels only this client and leaves the egg alive.
func AttachLocalIO(ctx context.Context, cfg *config.Config, sessionID string, input io.Reader, output io.Writer, options egg.AttachOptions) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resolved, ec, err := OpenLocalEgg(ctx, cfg, sessionID)
	if err != nil {
		return false, err
	}
	defer cmdutil.CloseWithLog("egg client", ec)
	sessionID = resolved.ID

	return AttachStreamIO(ctx, sessionID, input, output, options, func(ctx context.Context, options egg.AttachOptions) (SessionStream, error) {
		return ec.AttachSessionWithOptions(ctx, sessionID, options)
	})
}

// SessionStream is shared by direct egg RPC and authenticated wing streaming.
// open must bind blocking Send/Recv calls to its context's lifetime.
type SessionStream interface {
	Send(*pb.SessionMsg) error
	Recv() (*pb.SessionMsg, error)
}

// AttachStreamIO is terminal presentation only; open supplies the authorized API.
func AttachStreamIO(ctx context.Context, sessionID string, input io.Reader, output io.Writer, options egg.AttachOptions, open func(context.Context, egg.AttachOptions) (SessionStream, error)) (detached bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fd := -1
	if file, ok := input.(*os.File); ok {
		fd = int(file.Fd())
	}
	// Only a native terminal asks for this presentation. Browser/MCP and piped
	// callers retain the legacy stream; each transport negotiates support.
	if file, ok := output.(*os.File); ok && fd >= 0 && term.IsTerminal(fd) && term.IsTerminal(int(file.Fd())) {
		options.NativeTerminal = true
		defer func() {
			if _, resetErr := output.Write(egg.NativeTerminalReset()); resetErr != nil {
				err = errors.Join(err, fmt.Errorf("reset terminal modes: %w", resetErr))
			}
		}()
	}
	if !options.ReadOnly && fd >= 0 && term.IsTerminal(fd) {
		if cols, rows, err := term.GetSize(fd); err == nil {
			options.Rows, options.Cols = uint32(rows), uint32(cols)
		}
	}
	stream, err := open(ctx, options)
	if err != nil {
		return false, fmt.Errorf("attach session %s: %w", sessionID, err)
	}
	if closer, ok := stream.(io.Closer); ok {
		defer closer.Close()
	}
	stream = &serializedSessionStream{SessionStream: stream}

	if fd >= 0 && term.IsTerminal(fd) {
		diagnostic, _ := ctx.Value(attachDiagnosticsKey{}).(io.Writer)
		if diagnostic == nil {
			diagnostic = os.Stderr
		}
		mode := ""
		if options.ReadOnly {
			mode = " (read-only; no input or resize)"
		}
		fmt.Fprintf(diagnostic, "[attached to %s%s; Ctrl+B then q detaches; Ctrl+B twice sends Ctrl+B]\n", sessionID, mode)
		oldState, rawErr := term.MakeRaw(fd)
		if rawErr != nil {
			return false, fmt.Errorf("put terminal in raw mode: %w", rawErr)
		}
		defer func() {
			if restoreErr := term.Restore(fd, oldState); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore terminal: %w", restoreErr))
			}
		}()
	}
	// Native files use kqueue/epoll cancellation without closing the caller's
	// stdin. Non-file sources must finish reads or be closable on cancellation.
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return false, fmt.Errorf("open terminal input: %w", err)
	}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		if !reader.Cancel() {
			if closer, ok := input.(io.Closer); ok {
				_ = closer.Close()
			}
		}
		workers.Wait()
		if closeErr := reader.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close terminal reader: %w", closeErr))
		}
	}()

	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)
	resizeErrors := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-winchCh:
				if options.ReadOnly {
					continue
				}
				if cols, rows, sizeErr := term.GetSize(fd); sizeErr == nil {
					if err := stream.Send(&pb.SessionMsg{SessionId: sessionID, Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{Rows: uint32(rows), Cols: uint32(cols)}}}); err != nil {
						resizeErrors <- fmt.Errorf("resize rejected (session exit was not confirmed): %w", err)
						return
					}
				}
			}
		}
	}()

	resultCh := make(chan attachResult, 1)
	var detaching atomic.Bool
	workers.Add(1)
	go func() {
		defer workers.Done()
		receiveAttachedOutput(stream, output, resultCh, &detaching)
	}()

	inputCh := make(chan attachInputResult, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		sendAttachedInputMode(stream, sessionID, reader, inputCh, options.ReadOnly, &detaching)
	}()

	for {
		select {
		case result := <-inputCh:
			if result.err != nil {
				return false, &AttachConnectionError{SessionID: sessionID, Err: result.err}
			}
			// Queuing Detach does not confirm that the server processed earlier
			// input. Keep receiving until its ordered stream end acknowledges it.
			inputCh = nil
		case result := <-resultCh:
			if result.detached {
				if inputCh != nil {
					select {
					case sent := <-inputCh:
						if sent.err != nil {
							return false, &AttachConnectionError{SessionID: sessionID, Err: sent.err}
						}
					case <-ctx.Done():
						return false, ctx.Err()
					}
				}
				return true, nil
			}
			if result.err != nil {
				disconnectedID := sessionID
				if result.sessionID != "" {
					disconnectedID = result.sessionID
				}
				return false, &AttachConnectionError{SessionID: disconnectedID, Err: fmt.Errorf("attach session %s: %w", disconnectedID, result.err)}
			}
			if result.exitCode != nil && *result.exitCode != 0 {
				return false, &SessionExitError{Code: *result.exitCode}
			}
			return false, nil
		case err := <-resizeErrors:
			return false, &AttachConnectionError{SessionID: sessionID, Err: err}
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func receiveAttachedOutput(stream SessionStream, output io.Writer, resultCh chan<- attachResult, detaching ...*atomic.Bool) {
	var sessionID string
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) && len(detaching) > 0 && detaching[0] != nil && detaching[0].Load() {
				resultCh <- attachResult{detached: true}
				return
			}
			resultCh <- attachResult{sessionID: sessionID, err: fmt.Errorf("connection closed before session exit was confirmed: %w", err)}
			return
		}
		if msg.SessionId != "" && ValidateSessionID(msg.SessionId) == nil {
			sessionID = msg.SessionId
		}
		switch payload := msg.Payload.(type) {
		case *pb.SessionMsg_Output:
			if _, err := output.Write(payload.Output); err != nil {
				resultCh <- attachResult{sessionID: sessionID, err: fmt.Errorf("write terminal output: %w", err)}
				return
			}
		case *pb.SessionMsg_ExitCode:
			code := int(payload.ExitCode)
			resultCh <- attachResult{exitCode: &code}
			return
		}
	}
}

type attachInputResult struct {
	detached bool
	err      error
}

func sendAttachedInput(stream SessionStream, sessionID string, input io.Reader, resultCh chan<- attachInputResult, readOnly ...bool) {
	sendAttachedInputMode(stream, sessionID, input, resultCh, len(readOnly) > 0 && readOnly[0], nil)
}

func sendAttachedInputMode(stream SessionStream, sessionID string, input io.Reader, resultCh chan<- attachInputResult, readOnly bool, detaching *atomic.Bool) {
	filter := &AttachInputFilter{}
	buffer := make([]byte, 4096)
	for {
		n, err := input.Read(buffer)
		if n > 0 {
			output, detach := filter.Filter(buffer[:n])
			if len(output) > 0 && !readOnly {
				if sendErr := stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_Input{Input: output},
				}); sendErr != nil {
					resultCh <- attachInputResult{err: fmt.Errorf("send session input: %w", sendErr)}
					return
				}
			}
			if detach {
				if detaching != nil {
					detaching.Store(true)
				}
				sendErr := stream.Send(&pb.SessionMsg{
					SessionId: sessionID,
					Payload:   &pb.SessionMsg_Detach{Detach: true},
				})
				if sendErr != nil {
					resultCh <- attachInputResult{err: fmt.Errorf("detach session: %w", sendErr)}
				} else {
					resultCh <- attachInputResult{detached: true}
				}
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if tail := filter.Flush(); len(tail) > 0 && !readOnly {
					if sendErr := stream.Send(&pb.SessionMsg{SessionId: sessionID, Payload: &pb.SessionMsg_Input{Input: tail}}); sendErr != nil {
						resultCh <- attachInputResult{err: fmt.Errorf("send final session input: %w", sendErr)}
						return
					}
				}
				// A closed SSH input stream is a detached client, never a
				// request to stop its persistent session.
				if detaching != nil {
					detaching.Store(true)
				}
				sendErr := stream.Send(&pb.SessionMsg{SessionId: sessionID, Payload: &pb.SessionMsg_Detach{Detach: true}})
				if sendErr != nil {
					resultCh <- attachInputResult{err: fmt.Errorf("detach after input closed: %w", sendErr)}
				} else {
					resultCh <- attachInputResult{detached: true}
				}
			} else {
				resultCh <- attachInputResult{err: fmt.Errorf("read terminal input: %w", err)}
			}
			return
		}
	}
}
