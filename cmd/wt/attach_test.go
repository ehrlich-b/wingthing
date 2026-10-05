package main

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

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
			filter := &attachInputFilter{}
			var got []byte
			var detached bool
			for _, chunk := range tt.chunks {
				output, detach := filter.filter(chunk)
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
		if err := validateSessionID(valid); err != nil {
			t.Errorf("validateSessionID(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"", ".", "..", "../egg", "a/b", `a\b`, "two words", "x\ncommand", strings.Repeat("a", 129)} {
		if err := validateSessionID(invalid); err == nil {
			t.Errorf("validateSessionID(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got, want := shellQuote("/opt/wing thing/wt"), "'/opt/wing thing/wt'"; got != want {
		t.Fatalf("shellQuote path = %q, want %q", got, want)
	}
	if got, want := shellQuote("it's"), "'it'\"'\"'s'"; got != want {
		t.Fatalf("shellQuote apostrophe = %q, want %q", got, want)
	}
}
