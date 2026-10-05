package eggclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"

	"github.com/ehrlich-b/wingthing/internal/ws"

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
}

// filter returns bytes for the egg and whether the local client should detach.
// The prefix may arrive in a separate read from the following key.
func (f *AttachInputFilter) Filter(input []byte) ([]byte, bool) {
	output := make([]byte, 0, len(input)+1)
	for _, b := range input {
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

type attachResult struct {
	exitCode *int
	detached bool
	err      error
}

func AttachLocal(ctx context.Context, cfg *config.Config, sessionID string) (bool, error) {
	return AttachLocalOptions(ctx, cfg, sessionID, egg.AttachOptions{Claim: true, Owner: "cli"})
}

func AttachLocalOptions(ctx context.Context, cfg *config.Config, sessionID string, options egg.AttachOptions) (bool, error) {
	return attachLocalIO(ctx, cfg, sessionID, os.Stdin, os.Stdout, options)
}

// The attachment owns its stream lifetime, not the session process. Detaching
// or losing this connection cancels only this client and leaves the egg alive.
func attachLocalIO(ctx context.Context, cfg *config.Config, sessionID string, input io.Reader, output io.Writer, options egg.AttachOptions) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	resolved, ec, err := OpenLocalEgg(ctx, cfg, sessionID)
	if err != nil {
		return false, err
	}
	defer cmdutil.CloseWithLog("egg client", ec)
	sessionID = resolved.ID

	fd := -1
	if file, ok := input.(*os.File); ok {
		fd = int(file.Fd())
	}
	if !options.ReadOnly && fd >= 0 && term.IsTerminal(fd) {
		if cols, rows, err := term.GetSize(fd); err == nil {
			options.Rows, options.Cols = uint32(rows), uint32(cols)
		}
	}
	stream, err := ec.AttachSessionWithOptions(ctx, sessionID, options)
	if err != nil {
		return false, fmt.Errorf("attach session %s: %w", sessionID, err)
	}
	if !options.ReadOnly && fd >= 0 && term.IsTerminal(fd) {
		if cols, rows, sizeErr := term.GetSize(fd); sizeErr == nil {
			if resizeErr := ec.Resize(ctx, sessionID, uint32(rows), uint32(cols)); resizeErr != nil {
				return false, fmt.Errorf("resize session %s: %w", sessionID, resizeErr)
			}
		}
	}

	if fd >= 0 && term.IsTerminal(fd) {
		oldState, rawErr := term.MakeRaw(fd)
		if rawErr != nil {
			return false, fmt.Errorf("put terminal in raw mode: %w", rawErr)
		}
		defer func() {
			if err := term.Restore(fd, oldState); err != nil {
				log.Printf("restore terminal: %v", err)
			}
		}()
	}

	winchCh := make(chan os.Signal, 1)
	signal.Notify(winchCh, syscall.SIGWINCH)
	defer signal.Stop(winchCh)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-winchCh:
				if options.ReadOnly {
					continue
				}
				if cols, rows, sizeErr := term.GetSize(fd); sizeErr == nil {
					_ = ec.Resize(ctx, sessionID, uint32(rows), uint32(cols))
				}
			}
		}
	}()

	resultCh := make(chan attachResult, 1)
	var detaching atomic.Bool
	go receiveAttachedOutput(stream, output, resultCh, &detaching)

	inputCh := make(chan attachInputResult, 1)
	go sendAttachedInputMode(stream, sessionID, input, inputCh, options.ReadOnly, &detaching)

	for {
		select {
		case result := <-inputCh:
			if result.err != nil {
				return false, result.err
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
							return false, sent.err
						}
					case <-ctx.Done():
						return false, ctx.Err()
					}
				}
				return true, nil
			}
			if result.err != nil {
				return false, fmt.Errorf("attach session %s: %w; the session may still be running, reattach with its ID", sessionID, result.err)
			}
			if result.exitCode != nil && *result.exitCode != 0 {
				return false, fmt.Errorf("session exited with code %d", *result.exitCode)
			}
			return false, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func receiveAttachedOutput(stream pb.Egg_SessionClient, output io.Writer, resultCh chan<- attachResult, detaching ...*atomic.Bool) {
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) && len(detaching) > 0 && detaching[0] != nil && detaching[0].Load() {
				resultCh <- attachResult{detached: true}
				return
			}
			resultCh <- attachResult{err: fmt.Errorf("connection closed before session exit was confirmed: %w", err)}
			return
		}
		switch payload := msg.Payload.(type) {
		case *pb.SessionMsg_Output:
			if _, err := output.Write(payload.Output); err != nil {
				resultCh <- attachResult{err: fmt.Errorf("write terminal output: %w", err)}
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

func sendAttachedInput(stream pb.Egg_SessionClient, sessionID string, input io.Reader, resultCh chan<- attachInputResult, readOnly ...bool) {
	sendAttachedInputMode(stream, sessionID, input, resultCh, len(readOnly) > 0 && readOnly[0], nil)
}

func sendAttachedInputMode(stream pb.Egg_SessionClient, sessionID string, input io.Reader, resultCh chan<- attachInputResult, readOnly bool, detaching *atomic.Bool) {
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
