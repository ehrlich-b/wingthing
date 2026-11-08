package eggclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/controlsocket"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/sshcontrol"
)

// AttachRemoteIO keeps terminal ownership in this process while independently
// replacing lost SSH forwards. The saved profile is copied once, never reloaded.
func AttachRemoteIO(ctx context.Context, remote config.Remote, session string, input io.Reader, output, diagnostic io.Writer, options egg.AttachOptions, transport sshcontrol.Transport) (bool, error) {
	if input == nil {
		input = strings.NewReader("")
	}
	if output == nil {
		output = io.Discard
	}
	return attachRemoteIO(ctx, remote, session, input, output, diagnostic, options, transport, reconnectWait)
}

func attachRemoteIO(ctx context.Context, remote config.Remote, session string, input io.Reader, output, diagnostic io.Writer, options egg.AttachOptions, transport sshcontrol.Transport, pause reconnectPause) (bool, error) {
	return attachReconnectingIO(ctx, session, input, output, diagnostic, options, func(ctx context.Context, id string, exact bool, opts egg.AttachOptions) (SessionStream, error) {
		if remote.ControlSocket == "" {
			return openSSHAttachment(ctx, remote, transport.SSHPath, AttachmentRequest{Session: id, Exact: exact, Options: opts})
		}
		conn, err := transport.Dial(ctx, remote, controlsocket.Hello{})
		if err != nil {
			return nil, err
		}
		stream, _, err := controlsocket.DialAttachmentPath(ctx, conn.SocketPath(), controlsocket.Hello{WingID: remote.WingID, Attach: &controlsocket.Attachment{Session: id, Exact: exact, NativeTerminal: opts.NativeTerminal, ReadOnly: opts.ReadOnly, Takeover: opts.Takeover, Rows: opts.Rows, Cols: opts.Cols}})
		if err != nil {
			conn.Close()
			return nil, err
		}
		return &remoteAttachStream{SessionStream: stream, close: func() { stream.Close(); conn.Close() }}, nil
	}, pause)
}

type remoteAttachStream struct {
	SessionStream
	close func()
}

func (s *remoteAttachStream) Close() error { s.close(); return nil }

type reconnectOpen func(context.Context, string, bool, egg.AttachOptions) (SessionStream, error)
type reconnectPause func(context.Context, time.Duration) error

func reconnectWait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func retryAttachment(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, fatal := range []string{"permission denied", "authentication", "host key", "host identification", "too many authentication", "no supported authentication", "identity changed"} {
		if strings.Contains(text, fatal) {
			return false
		}
	}
	var network net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.As(err, &network) || strings.Contains(text, "ssh forward closed:") || strings.Contains(text, "connection reset") || strings.Contains(text, "broken pipe") || strings.Contains(text, "connection refused") || strings.Contains(text, "network is unreachable") || strings.Contains(text, "no route to host") || strings.Contains(text, "connection timed out")
}

type reconnectStream struct {
	ctx           context.Context
	cancel        context.CancelFunc
	open          reconnectOpen
	pause         reconnectPause
	opts          egg.AttachOptions
	diagnostic    io.Writer
	mu            sync.Mutex
	stream        SessionStream
	first         *pb.SessionMsg
	id            string
	detached      bool
	dropped       bool
	generation    uint64
	attemptCancel context.CancelFunc
}

func attachReconnectingIO(ctx context.Context, id string, input io.Reader, output, diagnostic io.Writer, opts egg.AttachOptions, open reconnectOpen, pause reconnectPause) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if diagnostic == nil {
		diagnostic = io.Discard
	}
	s := &reconnectStream{ctx: ctx, cancel: cancel, id: id, open: open, pause: pause, diagnostic: diagnostic}
	defer s.Close()
	return AttachStreamIO(WithAttachDiagnostics(ctx, diagnostic), id, input, output, opts, func(ctx context.Context, opts egg.AttachOptions) (SessionStream, error) {
		s.ctx, s.cancel = context.WithCancel(ctx)
		s.opts = opts
		if err := s.connect(ctx, false); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func closeAttachment(stream SessionStream) {
	if c, ok := stream.(io.Closer); ok {
		_ = c.Close()
	}
}
func (s *reconnectStream) Close() error {
	s.cancel()
	s.mu.Lock()
	stream := s.stream
	s.stream = nil
	s.generation++
	cancel := s.attemptCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stream != nil {
		closeAttachment(stream)
	}
	return nil
}
func (s *reconnectStream) connect(ctx context.Context, exact bool) error {
	attempt, cancel := context.WithCancel(ctx)
	deadline := time.AfterFunc(10*time.Second, cancel)
	defer deadline.Stop()
	s.mu.Lock()
	s.attemptCancel = cancel
	id := s.id
	opts := s.opts
	s.mu.Unlock()
	// The accepted stream uses the attachment lifetime, not the dial deadline.
	// Cancel the bounded attempt only when this stream is replaced or closed.
	timedOut := func(err error) error {
		if attempt.Err() != nil && ctx.Err() == nil {
			return os.ErrDeadlineExceeded
		}
		return err
	}
	stream, err := s.open(attempt, id, exact, opts)
	if err != nil {
		err = timedOut(err)
		cancel()
		return err
	}
	first, err := stream.Recv()
	if err == nil && (ValidateSessionID(first.SessionId) != nil || exact && first.SessionId != id) {
		err = errors.New("attachment returned a different session ID")
	}
	if err != nil {
		err = timedOut(err)
		closeAttachment(stream)
		cancel()
		return err
	}
	s.mu.Lock()
	s.id, s.stream, s.first, s.dropped = first.SessionId, stream, first, false
	s.generation++
	s.mu.Unlock()
	return nil
}
func (s *reconnectStream) lost(stream SessionStream) {
	s.mu.Lock()
	if s.stream != stream {
		s.mu.Unlock()
		return
	}
	s.stream = nil
	s.generation++
	cancel := s.attemptCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	closeAttachment(stream)
}
func (s *reconnectStream) Send(msg *pb.SessionMsg) error {
	s.mu.Lock()
	stream := s.stream
	id := s.id
	if size := msg.GetResize(); size != nil {
		s.opts.Rows, s.opts.Cols = size.Rows, size.Cols
	}
	if msg.GetDetach() {
		s.detached = true
	}
	detached := s.detached
	if stream == nil && len(msg.GetInput()) > 0 && !detached && !s.dropped {
		fmt.Fprint(s.diagnostic, "\r\n[offline input dropped]\r\n")
		s.dropped = true
	}
	s.mu.Unlock()
	if stream == nil {
		if detached {
			s.cancel()
		}
		return nil
	}
	msg.SessionId = id
	if err := stream.Send(msg); err != nil {
		s.lost(stream)
		s.mu.Lock()
		if !s.dropped && len(msg.GetInput()) > 0 && !detached {
			fmt.Fprint(s.diagnostic, "\r\n[offline input dropped]\r\n")
			s.dropped = true
		}
		s.mu.Unlock()
		if detached {
			s.cancel()
		}
	}
	return nil // Uncertain input is never replayed.
}
func (s *reconnectStream) Recv() (*pb.SessionMsg, error) {
	for {
		s.mu.Lock()
		first := s.first
		s.first = nil
		stream := s.stream
		detached := s.detached
		s.mu.Unlock()
		if first != nil {
			s.reportExit(first)
			return first, nil
		}
		if stream != nil {
			msg, err := stream.Recv()
			if err == nil {
				s.mu.Lock()
				id := s.id
				s.mu.Unlock()
				if msg.SessionId != "" && msg.SessionId != id {
					return nil, errors.New("attachment returned a different session ID")
				}
				s.reportExit(msg)
				return msg, nil
			}
			s.mu.Lock()
			alreadyLost := s.stream != stream
			detached = s.detached
			s.mu.Unlock()
			s.lost(stream)
			if detached {
				return nil, io.EOF
			}
			if !alreadyLost && !retryAttachment(err) && s.ctx.Err() == nil {
				return nil, err
			}
		}
		s.mu.Lock()
		detached = s.detached
		s.opts.Takeover = false
		s.mu.Unlock()
		if detached {
			return nil, io.EOF
		}
		fmt.Fprint(s.diagnostic, "\r\n[connection lost; reconnecting; Ctrl+B then q detaches]\r\n")
		delay := 250 * time.Millisecond
		for {
			if err := s.pause(s.ctx, delay); err != nil {
				s.mu.Lock()
				detached = s.detached
				s.mu.Unlock()
				if detached {
					return nil, io.EOF
				}
				return nil, err
			}
			err := s.connect(s.ctx, true)
			if err == nil {
				break
			}
			s.mu.Lock()
			detached = s.detached
			s.mu.Unlock()
			if detached {
				return nil, io.EOF
			}
			if !retryAttachment(err) {
				return nil, err
			}
			delay = min(2*delay, 5*time.Second)
		}
	}
}

// ConfirmedSessionExit accepts only the egg's explicit provider exit record.
func ConfirmedSessionExit(journal []byte) (int, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var code int
	var found bool
	for scanner.Scan() {
		var event egg.SessionEvent
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "session_exit" && event.Source == "egg_process" && event.ExitCode != nil {
			code, found = *event.ExitCode, true
		}
	}
	return code, found && scanner.Err() == nil
}

func (s *reconnectStream) reportExit(msg *pb.SessionMsg) {
	if code, ok := msg.Payload.(*pb.SessionMsg_ExitCode); ok {
		fmt.Fprintf(s.diagnostic, "\r\n[session exited with code %d]\r\n", code.ExitCode)
	}
}

func (s *reconnectStream) InputGeneration() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}
