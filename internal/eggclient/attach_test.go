package eggclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAttachInputFilter(t *testing.T) {
	tests := []struct {
		name   string
		chunks [][]byte
		want   []byte
		detach bool
	}{
		{name: "ordinary input", chunks: [][]byte{[]byte("hello")}, want: []byte("hello")},
		{name: "literal prefix", chunks: [][]byte{{attachPrefix, attachPrefix}}, want: []byte{attachPrefix}},
		{name: "unknown chord passes through", chunks: [][]byte{{attachPrefix, 'x'}}, want: []byte{attachPrefix, 'x'}},
		{name: "split literal prefix", chunks: [][]byte{{attachPrefix}, {attachPrefix}}, want: []byte{attachPrefix}},
		{name: "detach lower", chunks: [][]byte{{attachPrefix}, {'q'}}, detach: true},
		{name: "detach upper", chunks: [][]byte{{attachPrefix, 'Q'}}, detach: true},
		{name: "bytes before detach survive", chunks: [][]byte{{'a', attachPrefix, 'q'}}, want: []byte{'a'}, detach: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := &AttachInputFilter{}
			var got []byte
			var detached bool
			for _, chunk := range tt.chunks {
				output, detach := filter.Filter(chunk)
				got = append(got, output...)
				if detach {
					detached = true
					break
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("output = %v, want %v", got, tt.want)
			}
			if detached != tt.detach {
				t.Fatalf("detach = %v, want %v", detached, tt.detach)
			}
		})
	}
}

func TestAttachPastePreservesBytesAtEveryBoundary(t *testing.T) {
	want := []byte("雪🙂\x1b[200~paste\x02q\x02Q\x02\x02\xff\x1b[200~literal\x1b[201~tail")
	for split := 0; split <= len(want); split++ {
		f := &AttachInputFilter{}
		var got []byte
		for _, chunk := range [][]byte{want[:split], want[split:]} {
			out, detached := f.Filter(chunk)
			if detached {
				t.Fatalf("paste detached at boundary %d", split)
			}
			got = append(got, out...)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("boundary %d changed input: %q != %q", split, got, want)
		}
		if out, detached := f.Filter([]byte{2, 'q'}); !detached || len(out) != 0 {
			t.Fatalf("boundary %d did not leave paste mode", split)
		}
	}
	f := &AttachInputFilter{}
	var got []byte
	for _, b := range want {
		out, detach := f.Filter([]byte{b})
		if detach {
			t.Fatal("byte-at-a-time paste detached")
		}
		got = append(got, out...)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("byte-at-a-time input changed")
	}
	for range 10000 {
		if _, detach := f.Filter([]byte("\x1b[200~\x1b[20\x02q")); detach || f.marker > 5 {
			t.Fatal("unterminated paste detached or parser state grew")
		}
	}
}

func TestAttachClosedInputPreservesFinalPrefix(t *testing.T) {
	stream := &attachTestStream{}
	result := make(chan attachInputResult, 1)
	sendAttachedInput(stream, "full-id", bytes.NewReader([]byte{2}), result)
	if got := <-result; got.err != nil || !got.detached {
		t.Fatal(got)
	}
	if len(stream.sent) != 2 || !bytes.Equal(stream.sent[0].GetInput(), []byte{2}) || !stream.sent[1].GetDetach() {
		t.Fatalf("final input prefix lost: %v", stream.sent)
	}
}

type attachLiveStream struct {
	ctx         context.Context
	output      chan *pb.SessionMsg
	sent        chan *pb.SessionMsg
	receiveDone chan struct{}
	sendErr     error
}

func (s *attachLiveStream) Send(msg *pb.SessionMsg) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	select {
	case s.sent <- msg:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *attachLiveStream) Recv() (*pb.SessionMsg, error) {
	select {
	case msg, ok := <-s.output:
		if !ok {
			close(s.receiveDone)
			return nil, io.EOF
		}
		if _, exited := msg.Payload.(*pb.SessionMsg_ExitCode); exited {
			close(s.receiveDone)
		}
		return msg, nil
	case <-s.ctx.Done():
		close(s.receiveDone)
		return nil, s.ctx.Err()
	}
}

type attachFailWriter struct{}

func (attachFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAttachJoinsInputBeforeReturnOnEveryOutcome(t *testing.T) {
	for _, outcome := range []string{"eof", "exit", "nonzero", "write failure", "send failure", "cancel", "detach", "closed input"} {
		t.Run(outcome, func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ready := make(chan *attachLiveStream, 1)
			done := make(chan attachResult, 1)
			var output io.Writer = io.Discard
			if outcome == "write failure" {
				output = attachFailWriter{}
			}
			go func() {
				detached, err := AttachStreamIO(ctx, "full-id", input, output, egg.AttachOptions{}, func(ctx context.Context, _ egg.AttachOptions) (SessionStream, error) {
					s := &attachLiveStream{ctx: ctx, output: make(chan *pb.SessionMsg, 1), sent: make(chan *pb.SessionMsg, 8), receiveDone: make(chan struct{})}
					if outcome == "send failure" {
						s.sendErr = io.ErrClosedPipe
					}
					ready <- s
					return s, nil
				})
				done <- attachResult{detached: detached, err: err}
			}()
			s := <-ready
			switch outcome {
			case "eof":
				close(s.output)
			case "exit", "nonzero":
				code := int32(0)
				if outcome == "nonzero" {
					code = 17
				}
				s.output <- &pb.SessionMsg{Payload: &pb.SessionMsg_ExitCode{ExitCode: code}}
			case "write failure":
				s.output <- &pb.SessionMsg{Payload: &pb.SessionMsg_Output{Output: []byte("unwritable")}}
			case "send failure":
				_, _ = writer.Write([]byte("uncertain"))
			case "cancel":
				cancel()
			case "detach", "closed input":
				if outcome == "detach" {
					_, _ = writer.Write([]byte{2, 'q'})
				} else {
					_ = writer.Close()
				}
				select {
				case sent := <-s.sent:
					if !sent.GetDetach() {
						t.Fatal("missing detach")
					}
				case <-time.After(time.Second):
					t.Fatal("detach not sent")
				}
				close(s.output)
			}
			select {
			case got := <-done:
				if (outcome == "detach" || outcome == "closed input") != got.detached {
					t.Fatal(got)
				}
				if (outcome == "exit" || got.detached) != (got.err == nil) {
					t.Fatal(got)
				}
				if outcome == "nonzero" {
					var exit *SessionExitError
					if !errors.As(got.err, &exit) || exit.Code != 17 {
						t.Fatal(got.err)
					}
				}
			case <-time.After(3 * time.Second):
				t.Fatal("attachment failed to cancel and join its workers")
			}
			if outcome != "closed input" {
				_, _ = writer.Write([]byte("k"))
				key := make([]byte, 1)
				if _, err := io.ReadFull(input, key); err != nil || key[0] != 'k' {
					t.Fatalf("old input reader stole navigation key: %q %v", key, err)
				}
			}
		})
	}
}

type attachConcurrentStream struct {
	active  atomic.Int32
	overlap atomic.Bool
}

func (s *attachConcurrentStream) Send(*pb.SessionMsg) error {
	if s.active.Add(1) != 1 {
		s.overlap.Store(true)
	}
	defer s.active.Add(-1)
	runtime.Gosched()
	return nil
}

func TestAttachReconnectUsesConfirmedFullSessionID(t *testing.T) {
	stream := &attachTestStream{messages: []*pb.SessionMsg{{SessionId: "full-session-id", Payload: &pb.SessionMsg_Output{Output: []byte("snapshot")}}}}
	results := make(chan attachResult, 1)
	receiveAttachedOutput(stream, io.Discard, results)
	got := <-results
	if got.sessionID != "full-session-id" || got.err == nil {
		t.Fatalf("reconnect lost the full session ID: %+v", got)
	}
}
func (*attachConcurrentStream) Recv() (*pb.SessionMsg, error) { return nil, io.EOF }
func TestAttachSerializesResizeAndInput(t *testing.T) {
	transport := &attachConcurrentStream{}
	stream := &serializedSessionStream{SessionStream: transport}
	var workers sync.WaitGroup
	for range 100 {
		workers.Add(2)
		go func() {
			defer workers.Done()
			_ = stream.Send(&pb.SessionMsg{Payload: &pb.SessionMsg_Input{Input: []byte("x")}})
		}()
		go func() {
			defer workers.Done()
			_ = stream.Send(&pb.SessionMsg{Payload: &pb.SessionMsg_Resize{Resize: &pb.Resize{Rows: 24, Cols: 80}}})
		}()
	}
	workers.Wait()
	if transport.overlap.Load() {
		t.Fatal("concurrent transport sends")
	}
}

type attachTestStream struct {
	grpc.ClientStream
	messages []*pb.SessionMsg
	recvErr  error
	sent     []*pb.SessionMsg
	sendErr  error
}

func (s *attachTestStream) Recv() (*pb.SessionMsg, error) {
	if len(s.messages) == 0 {
		if s.recvErr != nil {
			return nil, s.recvErr
		}
		return nil, io.EOF
	}
	message := s.messages[0]
	s.messages = s.messages[1:]
	return message, nil
}

func (s *attachTestStream) Send(message *pb.SessionMsg) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, message)
	return nil
}

func TestAttachDoesNotReportLostConnectionAsSessionCompletion(t *testing.T) {
	for _, closed := range []error{io.EOF, status.Error(codes.Canceled, "transport canceled"), status.Error(codes.Unavailable, "socket disappeared")} {
		stream := &attachTestStream{recvErr: closed}
		results := make(chan attachResult, 1)
		receiveAttachedOutput(stream, io.Discard, results)
		result := <-results
		if result.err == nil || result.exitCode != nil || !strings.Contains(result.err.Error(), "before session exit was confirmed") {
			t.Fatalf("connection close %v incorrectly completed attachment: %+v", closed, result)
		}
	}
}

func TestAttachCompletionRequiresExplicitExitEvent(t *testing.T) {
	stream := &attachTestStream{messages: []*pb.SessionMsg{
		{Payload: &pb.SessionMsg_Output{Output: []byte("final result\n")}},
		{Payload: &pb.SessionMsg_ExitCode{ExitCode: 17}},
	}}
	var output bytes.Buffer
	results := make(chan attachResult, 1)
	receiveAttachedOutput(stream, &output, results)
	result := <-results
	if result.err != nil || result.exitCode == nil || *result.exitCode != 17 || output.String() != "final result\n" {
		t.Fatalf("explicit completion result=%+v output=%q", result, output.String())
	}
}

func TestAttachIntentionalDetachRequiresOrderedStreamEnd(t *testing.T) {
	var intent atomic.Bool
	intent.Store(true)
	stream := &attachTestStream{messages: []*pb.SessionMsg{{Payload: &pb.SessionMsg_Output{Output: []byte("last output")}}}}
	results := make(chan attachResult, 1)
	var output bytes.Buffer
	receiveAttachedOutput(stream, &output, results, &intent)
	result := <-results
	if !result.detached || result.err != nil || result.exitCode != nil || output.String() != "last output" {
		t.Fatalf("ordered detach result=%+v output=%q", result, output.String())
	}
	for _, closed := range []error{status.Error(codes.Canceled, "transport canceled"), status.Error(codes.Unavailable, "lost connection")} {
		receiveAttachedOutput(&attachTestStream{recvErr: closed}, io.Discard, results, &intent)
		if result := <-results; result.detached || result.err == nil {
			t.Fatalf("detach intent hid lost transport: %+v", result)
		}
	}
}

func TestAttachClosedInputDetachesWithoutStoppingSession(t *testing.T) {
	stream := &attachTestStream{}
	results := make(chan attachInputResult, 1)
	sendAttachedInput(stream, "exact-session", strings.NewReader("input\r"), results)
	result := <-results
	if !result.detached || result.err != nil || len(stream.sent) != 2 {
		t.Fatalf("closed input result=%+v messages=%v", result, stream.sent)
	}
	if stream.sent[0].SessionId != "exact-session" || string(stream.sent[0].GetInput()) != "input\r" || !stream.sent[1].GetDetach() {
		t.Fatalf("closed input did not send input then detach: %v", stream.sent)
	}
}

func TestAttachReportsInputTransportFailure(t *testing.T) {
	stream := &attachTestStream{sendErr: io.ErrClosedPipe}
	results := make(chan attachInputResult, 1)
	sendAttachedInput(stream, "session", strings.NewReader("data"), results)
	result := <-results
	if result.detached || !errors.Is(result.err, io.ErrClosedPipe) {
		t.Fatalf("lost input transport was hidden: %+v", result)
	}
}

func TestValidateSessionID(t *testing.T) {
	for _, valid := range []string{"deadbeef", "session-1", "agent_one", "a.b"} {
		if err := ValidateSessionID(valid); err != nil {
			t.Errorf("validateSessionID(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"", ".", "..", "../egg", "a/b", `a\b`, "two words", "x\ncommand", strings.Repeat("a", 129)} {
		if err := ValidateSessionID(invalid); err == nil {
			t.Errorf("validateSessionID(%q) unexpectedly succeeded", invalid)
		}
	}
}
