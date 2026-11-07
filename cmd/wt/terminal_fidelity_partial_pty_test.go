package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/creack/pty"
	"github.com/ehrlich-b/wingthing/internal/egg"
	pb "github.com/ehrlich-b/wingthing/internal/egg/pb"
	"github.com/ehrlich-b/wingthing/internal/eggclient"
)

type fidelityPartialStep struct{ data, want string }

type fidelityPartialStream struct {
	output *os.File
	steps  []fidelityPartialStep
	next   int
}

func (*fidelityPartialStream) Send(*pb.SessionMsg) error { return nil }

func (s *fidelityPartialStream) Recv() (*pb.SessionMsg, error) {
	if s.next > 0 {
		// Recv is called only after the previous output Write returns. A fence
		// written directly to the PTY makes withheld bytes fail deterministically,
		// without waiting for a timeout or completing the writer's partial CSI.
		if _, err := fmt.Fprintf(s.output, "\x18WRITE_DONE_%d\r\n", s.next-1); err != nil {
			return nil, err
		}
	}
	if s.next == len(s.steps) {
		return &pb.SessionMsg{Payload: &pb.SessionMsg_ExitCode{}}, nil
	}
	data := s.steps[s.next].data
	s.next++
	return &pb.SessionMsg{Payload: &pb.SessionMsg_Output{Output: []byte(data)}}, nil
}

func TestTerminalFidelityPartialSequences(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	h := &fidelityTerminal{
		t: t, master: master, slave: slave,
		modes: map[int]bool{25: true}, saved: map[int]bool{},
		changed: make(chan struct{}, 1), readDone: make(chan struct{}), processDone: make(chan struct{}),
		queries: map[string]string{"\x1b[6n": "\x1b[7;13R"},
	}
	go h.read()
	t.Cleanup(func() { master.Close(); slave.Close(); <-h.readDone })
	steps := []fidelityPartialStep{
		{"PARTIAL:\x1b[?100", "PARTIAL:\x1b[?100"},
		{"4h\x1b", "4h\x1b"},
		{"[", "["},
		{"?1004", "?1004"},
		{"s", "\x18"}, // Cancel the forwarded prefix; keep the outer save slot.
		{"\x1b[?1004l\x1b[?1004", "\x1b[?1004l\x1b[?1004"},
		{"r", "\x18\x1b[?1004h"}, // Restore the provider's saved enabled state.
		{"\x1b[?1004\a", "\x1b[?1004\a"},
		{"s", "\x18"}, // The already forwarded bell must not execute twice.
		{"\x1b[?1004l\x1b[?1004", "\x1b[?1004l\x1b[?1004"},
		{"r", "\x18\x1b[?1004h"},
	}
	stream := &fidelityPartialStream{output: slave, steps: steps}
	ctx := eggclient.WithAttachDiagnostics(t.Context(), io.Discard)
	_, err = eggclient.AttachStreamIO(ctx, "partial", slave, slave, egg.AttachOptions{},
		func(context.Context, egg.AttachOptions) (eggclient.SessionStream, error) { return stream, nil })
	if err != nil {
		t.Fatal(err)
	}
	h.wait(fmt.Sprintf("WRITE_DONE_%d\r\n", len(steps)-1))
	wire := h.snapshot()
	fidelityWrite(t, os.Getenv("WT_FIDELITY_EVIDENCE"), "partial-sequences.ansi", wire)
	start := bytes.Index(wire, []byte("PARTIAL:"))
	if start < 0 {
		t.Fatalf("missing provider output: %q", wire)
	}
	var want string
	for i, step := range steps {
		want += step.want + fmt.Sprintf("\x18WRITE_DONE_%d\r\n", i)
	}
	if !bytes.HasPrefix(wire[start:], []byte(want)) {
		t.Fatalf("output withheld or split mode snapshot changed:\n got %q\nwant %q", wire[start:], want)
	}
	t.Log("each partial write reached the PTY before the next Recv; split saves/restores retained provider state without replaying controls")
}
