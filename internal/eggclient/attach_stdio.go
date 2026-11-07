package eggclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/ehrlich-b/wingthing/internal/cmdutil"
	"github.com/ehrlich-b/wingthing/internal/config"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/remote"
	"github.com/muesli/cancelreader"
	"google.golang.org/protobuf/encoding/protojson"
)

// AttachmentRequest is private to the two wt peers; terminal data is framed,
// so a provider cannot impersonate a handshake or an exit using screen text.
type AttachmentRequest struct {
	Session string            `json:"session"`
	Exact   bool              `json:"exact"`
	Options egg.AttachOptions `json:"options"`
}
type attachmentFrame struct {
	Message json.RawMessage `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`
	End     bool            `json:"end,omitempty"`
}
type attachmentWire struct {
	scanner *bufio.Scanner
	encoder *json.Encoder
	mu      sync.Mutex
}

func newAttachmentWire(input io.Reader, output io.Writer) *attachmentWire {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		// Frames are newline terminated. Preserve transport truncation instead
		// of handing an incomplete final frame to the JSON decoder.
		if atEOF && advance > 0 && data[advance-1] != '\n' {
			return 0, nil, io.ErrUnexpectedEOF
		}
		return advance, token, err
	})
	return &attachmentWire{scanner: scanner, encoder: json.NewEncoder(output)}
}
func (s *attachmentWire) frame(frame attachmentFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encoder.Encode(frame)
}
func (s *attachmentWire) Send(msg *pb.SessionMsg) error {
	data, err := protojson.Marshal(msg)
	if err != nil {
		return err
	}
	return s.frame(attachmentFrame{Message: data})
}
func (s *attachmentWire) Recv() (*pb.SessionMsg, error) {
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	var frame attachmentFrame
	if err := json.Unmarshal(s.scanner.Bytes(), &frame); err != nil {
		return nil, err
	}
	if frame.Error != "" {
		return nil, errors.New(frame.Error)
	}
	if frame.End {
		return nil, io.EOF
	}
	msg := new(pb.SessionMsg)
	if err := protojson.Unmarshal(frame.Message, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

type sshAttachment struct {
	*attachmentWire
	cancel     context.CancelFunc
	input      *os.File
	output     *os.File
	done       chan struct{}
	result     error
	diagnostic *remoteSessionBuffer
	once       sync.Once
}

func (s *sshAttachment) Close() error {
	s.once.Do(func() { s.cancel(); s.input.Close(); s.output.Close(); <-s.done })
	return nil
}
func (s *sshAttachment) Recv() (*pb.SessionMsg, error) {
	msg, err := s.attachmentWire.Recv()
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return msg, err
	}
	<-s.done
	result := s.result
	if result == nil {
		return nil, err
	}
	diagnostic := s.diagnostic.buffer.String()
	var exit *cmdutil.CommandExitError
	if errors.As(result, &exit) && exit.Code == 255 {
		return nil, fmt.Errorf("SSH forward closed: %s", diagnostic)
	}
	return nil, fmt.Errorf("remote attachment: %w: %s", result, diagnostic)
}
func openSSHAttachment(ctx context.Context, profile config.Remote, path string, request AttachmentRequest) (SessionStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	input, writer, err := os.Pipe()
	if err != nil {
		cancel()
		return nil, err
	}
	reader, output, err := os.Pipe()
	if err != nil {
		input.Close()
		writer.Close()
		cancel()
		return nil, err
	}
	// An SSH helper can retain stdout after SSH exits. Unblock Recv before
	// terminal cleanup joins its output worker on cancellation.
	context.AfterFunc(ctx, func() { _ = reader.Close() })
	s := &sshAttachment{attachmentWire: newAttachmentWire(reader, writer), cancel: cancel, input: writer, output: reader, done: make(chan struct{}), diagnostic: &remoteSessionBuffer{limit: 64 << 10, stream: "SSH diagnostic", cancel: cancel}}
	invocation := remote.ProfileInvocation(profile, []string{"attach", "--attachment-stream"})
	invocation.NonInteractive = true
	invocation.StrictHostKeys = true
	go func() {
		err := remote.RunRemoteInvocation(ctx, invocation, remote.IO{In: input, Out: output, ErrOut: s.diagnostic, SSHPath: path})
		input.Close()
		output.Close()
		s.result = err
		close(s.done)
	}()
	if err := s.encoder.Encode(request); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// ServeAttachmentIO bridges the receiving peer's authorized stream without
// terminal interpretation. Losing this process never stops its persistent egg.
func ServeAttachmentIO(ctx context.Context, input io.Reader, output io.Writer, open func(context.Context, AttachmentRequest) (SessionStream, error)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return err
	}
	wire := newAttachmentWire(reader, output)
	if !wire.scanner.Scan() {
		reader.Close()
		return io.ErrUnexpectedEOF
	}
	var request AttachmentRequest
	if err := json.Unmarshal(wire.scanner.Bytes(), &request); err != nil {
		reader.Close()
		return err
	}
	stream, err := open(ctx, request)
	if err != nil {
		reader.Close()
		_ = wire.frame(attachmentFrame{Error: err.Error()})
		return err
	}
	var workers sync.WaitGroup
	defer func() { cancel(); reader.Cancel(); closeAttachment(stream); workers.Wait(); reader.Close() }()
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			msg, err := wire.Recv()
			if err != nil {
				cancel()
				return
			}
			if msg.AttachOptions != nil || msg.AttachmentInfo != nil {
				cancel()
				return
			}
			switch msg.Payload.(type) {
			case *pb.SessionMsg_Input, *pb.SessionMsg_Resize, *pb.SessionMsg_Detach:
			default:
				cancel()
				return
			}
			if err := stream.Send(msg); err != nil {
				cancel()
				return
			}
			if msg.GetDetach() {
				return
			}
		}
	}()
	for {
		msg, err := stream.Recv()
		if err != nil {
			frame := attachmentFrame{End: true}
			if !errors.Is(err, io.EOF) {
				frame.Error = err.Error()
			}
			_ = wire.frame(frame)
			return err
		}
		msg.AttachmentInfo = nil
		if err := wire.Send(msg); err != nil {
			return err
		}
		if _, ok := msg.Payload.(*pb.SessionMsg_ExitCode); ok {
			return nil
		}
	}
}
